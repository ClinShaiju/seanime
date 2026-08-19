import { app, BrowserWindow, ipcMain, screen } from "electron"
import * as fs from "node:fs/promises"
import * as path from "node:path"
import { log } from "../logging"
import { MpvNativeBounds, MpvNativeCreateOptions, MpvNativeSession } from "./session"

const sessions = new Map<string, MpvNativeSession>()
/**
 * Create and destroy for one player must not interleave. A destroy arriving while a create is still running
 * finds nothing in `sessions` and no-ops, and the create then registers a session nobody owns — an orphan
 * mpv process plus its window. React remounts (playerId / warm-epoch changes) hit exactly this window.
 */
const operations = new Map<string, Promise<unknown>>()

function serialize<T>(playerId: string, operation: () => Promise<T>): Promise<T> {
    const previous = operations.get(playerId) ?? Promise.resolve()
    const result = previous.then(operation, operation)
    const tail = result.then(() => undefined, () => undefined)
    operations.set(playerId, tail)
    void tail.then(() => {
        if (operations.get(playerId) === tail) operations.delete(playerId)
    })
    return result
}

/** True when the native mpv window backend can run at all. Windows only for now — see mpv-native-window.md. */
export function isMpvNativeSupported(): boolean {
    return process.platform === "win32"
}

function sendEvent(window: BrowserWindow, playerId: string, payload: Record<string, unknown>): void {
    if (window.isDestroyed() || window.webContents.isDestroyed()) return
    window.webContents.send("mpvnative:event", { playerId, ...payload })
}

async function destroySession(playerId: string): Promise<void> {
    const session = sessions.get(playerId)
    if (!session) return
    sessions.delete(playerId)
    await session.destroy()
}

function requireSession(playerId: string): MpvNativeSession {
    const session = sessions.get(playerId)
    if (!session) throw new Error(`no native mpv session for player ${playerId}`)
    return session
}

/**
 * @param isEnabled reports the user setting; the window is only created transparent when it was on at
 * startup, so the renderer must see the same answer for the whole session.
 */
export function registerMpvNativeIpc(isEnabled: () => boolean): void {
    ipcMain.handle("mpvnative:supported", () => isMpvNativeSupported() && isEnabled())

    // The renderer has to know the backend before it renders the player, and an async probe can lose that
    // race (the answer would silently fall back to mpv-prism for the whole session). One blocking call at
    // module load is cheap and removes the race entirely.
    ipcMain.on("mpvnative:supported-sync", (event: Electron.IpcMainEvent) => {
        event.returnValue = isMpvNativeSupported() && isEnabled()
    })

    ipcMain.handle("mpvnative:display-frequency", (event: Electron.IpcMainInvokeEvent) => {
        const window = BrowserWindow.fromWebContents(event.sender)
        const bounds = window && !window.isDestroyed() ? window.getBounds() : null
        const display = bounds ? screen.getDisplayMatching(bounds) : screen.getPrimaryDisplay()
        return display.displayFrequency || 60
    })

    ipcMain.handle("mpvnative:create", async (event: Electron.IpcMainInvokeEvent, playerId: string, options: MpvNativeCreateOptions) => {
        if (!isMpvNativeSupported()) throw new Error("native mpv playback is not supported on this platform")
        const window = BrowserWindow.fromWebContents(event.sender)
        if (!window) throw new Error("native mpv playback requires a window")

        await serialize(playerId, async () => {
            await destroySession(playerId)
            const session = await MpvNativeSession.create(
                playerId,
                window,
                options ?? {},
                mpvEvent => sendEvent(window, playerId, mpvEvent),
                reason => {
                    sendEvent(window, playerId, { event: "shutdown", reason })
                    void destroySession(playerId)
                },
            )
            sessions.set(playerId, session)
            log.info(`[mpv-native] session ready for ${playerId}`)
        })
    })

    ipcMain.handle("mpvnative:destroy", async (_: Electron.IpcMainInvokeEvent, playerId: string) => {
        await serialize(playerId, () => destroySession(playerId))
    })

    ipcMain.handle("mpvnative:command", async (_: Electron.IpcMainInvokeEvent, playerId: string, args: unknown[]) => {
        return requireSession(playerId).command(args)
    })

    ipcMain.handle("mpvnative:get-property", async (_: Electron.IpcMainInvokeEvent, playerId: string, name: string) => {
        return requireSession(playerId).getProperty(name)
    })

    ipcMain.handle("mpvnative:set-property", async (_: Electron.IpcMainInvokeEvent, playerId: string, name: string, value: unknown) => {
        await requireSession(playerId).setProperty(name, value)
    })

    ipcMain.handle("mpvnative:observe-property", async (_: Electron.IpcMainInvokeEvent, playerId: string, name: string) => {
        await requireSession(playerId).observeProperty(name)
    })

    // There is no frame in the DOM to grab on this backend, so mpv writes the screenshot itself
    ipcMain.handle("mpvnative:screenshot", async (_: Electron.IpcMainInvokeEvent, playerId: string) => {
        const session = requireSession(playerId)
        const file = path.join(app.getPath("temp"), `seanime-mpv-screenshot-${Date.now()}.png`)
        try {
            await session.command(["screenshot-to-file", file, "video"])
            return (await fs.readFile(file)).toString("base64")
        }
        finally {
            await fs.rm(file, { force: true }).catch(() => undefined)
        }
    })

    ipcMain.on("mpvnative:set-video-rect", (_: Electron.IpcMainEvent, playerId: string, rect: MpvNativeBounds) => {
        sessions.get(playerId)?.setVideoRect(rect)
    })

    ipcMain.on("mpvnative:set-visible", (_: Electron.IpcMainEvent, playerId: string, visible: boolean) => {
        sessions.get(playerId)?.setVisible(visible)
    })
}

export async function disposeMpvNative(): Promise<void> {
    await Promise.all([...sessions.keys()].map(playerId => destroySession(playerId).catch(error => {
        log.warn(`[mpv-native] failed to destroy ${playerId}: ${String(error)}`)
    })))
}

/** Quit path: Electron force-exits 500 ms after shutdown starts, which the graceful destroy cannot win. */
export function disposeMpvNativeNow(): void {
    for (const [playerId, session] of [...sessions]) {
        sessions.delete(playerId)
        try {
            session.killNow()
        }
        catch (error) {
            log.warn(`[mpv-native] failed to kill ${playerId}: ${String(error)}`)
        }
    }
}
