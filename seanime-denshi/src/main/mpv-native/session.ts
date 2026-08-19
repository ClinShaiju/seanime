import { app, BrowserWindow, screen } from "electron"
import { ChildProcess, spawn } from "node:child_process"
import * as fs from "node:fs"
import * as path from "node:path"
import { log } from "../logging"
import { MpvIpcClient, MpvIpcEvent } from "./ipc"

export type MpvNativeCreateOptions = {
    /** mpv options applied at startup, e.g. `{ hwdec: "auto-safe" }`. */
    options?: Record<string, string | number | boolean>
    /** Extra config files to include (the UI writes the user's custom mpv config to disk). */
    configFiles?: string[]
    /** Properties to observe; changes arrive as `property-change` events. */
    observe?: string[]
}

export type MpvNativeBounds = { x: number, y: number, width: number, height: number }

const IPC_CONNECT_TIMEOUT_MS = 15_000
const QUIT_GRACE_MS = 2_000

/** Options the UI must not be able to set: they would break embedding, input routing or the IPC channel. */
const FORBIDDEN_OPTIONS = new Set([
    "wid", "input-ipc-server", "terminal", "input-terminal", "input-vo-keyboard", "input-default-bindings",
    "vo", "config", "config-dir", "include", "script", "scripts", "load-scripts", "input-conf", "log-file",
])

function resolveMpvBinary(): string {
    const candidates = app.isPackaged
        ? [path.join(process.resourcesPath, "binaries", "mpv", "mpv.exe")]
        : [
            path.join(app.getAppPath(), "binaries", "mpv", "mpv.exe"),
            path.join(process.cwd(), "binaries", "mpv", "mpv.exe"),
        ]
    for (const candidate of candidates) {
        if (fs.existsSync(candidate)) return candidate
    }
    throw new Error(`mpv binary not found (looked in: ${candidates.join(", ")}). Run scripts/fetch-mpv.mjs.`)
}

function windowHandleToWid(window: BrowserWindow): string {
    const handle = window.getNativeWindowHandle()
    // Windows HWNDs are pointer-sized; mpv wants the numeric value as a decimal string
    const value = handle.length >= 8 ? handle.readBigUInt64LE(0) : BigInt(handle.readUInt32LE(0))
    return value.toString()
}

/**
 * One mpv process rendering into its own window, with the Seanime UI window re-parented on top of it.
 *
 * The video window is the parent because Electron always stacks a child window above its parent, and the UI
 * window has to be the one on top. mpv fills the video window and letterboxes the video itself.
 */
export class MpvNativeSession {
    private readonly ipc = new MpvIpcClient()
    private process: ChildProcess | null = null
    private videoWindow: BrowserWindow | null = null
    private readonly observedIds = new Map<number, string>()
    private nextObserveId = 1
    private destroyed = false
    private videoRect: MpvNativeBounds | null = null
    private readonly syncBounds = () => this.applyBounds()

    private constructor(
        readonly playerId: string,
        private readonly uiWindow: BrowserWindow,
        private readonly onEvent: (event: MpvIpcEvent) => void,
        private readonly onExit: (reason: string) => void,
    ) {}

    static async create(
        playerId: string,
        uiWindow: BrowserWindow,
        options: MpvNativeCreateOptions,
        onEvent: (event: MpvIpcEvent) => void,
        onExit: (reason: string) => void,
    ): Promise<MpvNativeSession> {
        const session = new MpvNativeSession(playerId, uiWindow, onEvent, onExit)
        try {
            await session.start(options)
        }
        catch (error) {
            await session.destroy()
            throw error
        }
        return session
    }

    private async start(options: MpvNativeCreateOptions): Promise<void> {
        const binary = resolveMpvBinary()

        const uiBounds = this.uiWindow.getContentBounds()
        this.videoWindow = new BrowserWindow({
            x: uiBounds.x, y: uiBounds.y, width: uiBounds.width, height: uiBounds.height,
            show: false,
            frame: false,
            // The host must paint nothing: a Chromium-painted window fights mpv's child HWND for the
            // surface and ends up covering the video (verified in tests/mpv-native-spike.mjs). mpv itself
            // clears the window to black, so there is no see-through gap once it is running.
            transparent: true,
            backgroundColor: "#00000000",
            title: "Seanime",
            skipTaskbar: true,
            webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true },
        })
        this.videoWindow.setMenu(null)

        const pipePath = `\\\\.\\pipe\\seanime-mpv-${process.pid}-${this.playerId.replace(/[^a-zA-Z0-9_-]/g, "")}`
        const args = this.buildArgs(pipePath, options)
        log.info(`[mpv-native] starting ${binary} for ${this.playerId}`)

        this.process = spawn(binary, args, { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] })
        this.process.stderr?.setEncoding("utf8")
        this.process.stderr?.on("data", chunk => {
            const text = String(chunk).trim()
            if (text) log.warn(`[mpv-native] mpv: ${text}`)
        })
        this.process.on("exit", (code, signal) => {
            const reason = `mpv exited (code=${code}, signal=${signal})`
            log.info(`[mpv-native] ${reason}`)
            this.process = null
            if (!this.destroyed) this.onExit(reason)
        })
        this.process.on("error", error => {
            log.error(`[mpv-native] failed to start mpv: ${error.message}`)
            if (!this.destroyed) this.onExit(error.message)
        })

        await this.ipc.connect(pipePath, IPC_CONNECT_TIMEOUT_MS)
        this.ipc.onEvent(event => this.handleEvent(event))

        // The UI window becomes a child so it is composited above the video window and follows it around.
        // moveTop is required: creating mpv's child HWND leaves the host above the UI window otherwise.
        this.videoWindow.show()
        this.uiWindow.setParentWindow(this.videoWindow)
        this.uiWindow.moveTop()
        this.uiWindow.focus()

        // A ResizeObserver in the renderer only reports element changes, so window moves are tracked here
        this.uiWindow.on("move", this.syncBounds)
        this.uiWindow.on("resize", this.syncBounds)
        this.uiWindow.on("enter-full-screen", this.syncBounds)
        this.uiWindow.on("leave-full-screen", this.syncBounds)

        for (const name of options.observe ?? []) await this.observeProperty(name)
    }

    private buildArgs(pipePath: string, options: MpvNativeCreateOptions): string[] {
        const args = [
            `--wid=${windowHandleToWid(this.videoWindow!)}`,
            `--input-ipc-server=${pipePath}`,
            "--idle=yes",
            "--force-window=yes",
            "--keep-open=yes",
            "--no-terminal",
            "--no-config",
            // The Seanime UI owns all input; mpv must not grab keys, drag its window or draw its own OSD
            "--no-input-default-bindings",
            "--input-vo-keyboard=no",
            "--no-window-dragging",
            "--osc=no",
            "--osd-level=0",
            "--audio-client-name=Seanime",
            "--title=Seanime",
        ]

        // mpv renders into a window we own, so it cannot query the monitor itself. Display sync is worthless
        // without a real refresh rate, so hand it the rate of whichever display the UI window is on.
        if (!(options.options && "display-fps-override" in options.options)) {
            const display = screen.getDisplayMatching(this.uiWindow.getBounds())
            args.push(`--display-fps-override=${display.displayFrequency || 60}`)
        }

        for (const file of options.configFiles ?? []) {
            if (fs.existsSync(file)) args.push(`--include=${file}`)
        }

        for (const [name, value] of Object.entries(options.options ?? {})) {
            if (FORBIDDEN_OPTIONS.has(name)) {
                log.warn(`[mpv-native] ignoring forbidden option ${name}`)
                continue
            }
            args.push(`--${name}=${String(value)}`)
        }

        return args
    }

    private handleEvent(event: MpvIpcEvent): void {
        if (event.event === "property-change" && typeof event.id === "number") {
            // mpv echoes the observe id; restore the name so the renderer sees prism-shaped property events
            const name = this.observedIds.get(event.id)
            if (name) event.name = name
        }
        this.onEvent(event)
    }

    command(args: unknown[]): Promise<unknown> {
        return this.ipc.command(args)
    }

    async observeProperty(name: string): Promise<void> {
        const id = this.nextObserveId++
        this.observedIds.set(id, name)
        await this.ipc.command(["observe_property", id, name])
    }

    async getProperty(name: string): Promise<unknown> {
        return this.ipc.command(["get_property", name])
    }

    async setProperty(name: string, value: unknown): Promise<void> {
        await this.ipc.command(["set_property", name, value])
    }

    /**
     * Positions the video window under the UI's video element. The rect is in CSS pixels relative to the UI
     * window's content area, which matches Electron's DIP bounds as long as the page zoom factor is 1.
     */
    setVideoRect(rect: MpvNativeBounds): void {
        this.videoRect = rect
        this.applyBounds()
    }

    private applyBounds(): void {
        if (!this.videoWindow || this.videoWindow.isDestroyed() || this.uiWindow.isDestroyed()) return
        const rect = this.videoRect
        if (!rect) return
        const content = this.uiWindow.getContentBounds()
        this.videoWindow.setBounds({
            x: Math.round(content.x + rect.x),
            y: Math.round(content.y + rect.y),
            width: Math.max(2, Math.round(rect.width)),
            height: Math.max(2, Math.round(rect.height)),
        })
    }

    setVisible(visible: boolean): void {
        if (!this.videoWindow || this.videoWindow.isDestroyed()) return
        if (visible) {
            this.videoWindow.showInactive()
            this.uiWindow.moveTop()
        }
        else {
            this.videoWindow.hide()
        }
    }

    async destroy(): Promise<void> {
        if (this.destroyed) return
        this.destroyed = true

        if (this.ipc.connected) {
            try {
                await this.ipc.command(["quit"])
            }
            catch {
                // mpv may already be gone
            }
        }
        this.ipc.dispose()

        const child = this.process
        this.process = null
        if (child && child.exitCode === null) {
            await new Promise<void>(resolve => {
                const timer = setTimeout(() => {
                    try {
                        child.kill()
                    }
                    catch {
                        // already gone
                    }
                    resolve()
                }, QUIT_GRACE_MS)
                child.once("exit", () => {
                    clearTimeout(timer)
                    resolve()
                })
            })
        }

        // Detach before closing the parent, otherwise closing it takes the UI window down with it
        if (!this.uiWindow.isDestroyed()) {
            this.uiWindow.off("move", this.syncBounds)
            this.uiWindow.off("resize", this.syncBounds)
            this.uiWindow.off("enter-full-screen", this.syncBounds)
            this.uiWindow.off("leave-full-screen", this.syncBounds)
            this.uiWindow.setParentWindow(null)
        }
        if (this.videoWindow && !this.videoWindow.isDestroyed()) this.videoWindow.destroy()
        this.videoWindow = null
    }
}
