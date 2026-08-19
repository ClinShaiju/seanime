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
/** Verbose mpv output is only forwarded for these; everything else is kept to warnings and errors. */
const LOGGED_MPV_PREFIXES = ["vo", "ao", "vd", "ad", "cplayer", "video", "audio"]

type MpvLogMessage = { prefix?: string, level?: string, text?: string }

/**
 * How far the display/video FPS ratio has to sit from a whole number before interpolation earns its cost.
 * 24 fps on a 143.8 Hz panel is 5.998 - already an even 6-vsync cadence, nothing to smooth.
 */
const INTERPOLATION_RATIO_TOLERANCE = 0.05

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
 * One mpv process rendering into its own window, kept directly beneath the Seanime UI window.
 *
 * Both windows are top-level and are raised as a pair: making the UI window a child of the video window costs
 * the app its taskbar and alt-tab entry, and did not reliably keep it on top either. mpv fills the video
 * window and letterboxes the video itself.
 */
export class MpvNativeSession {
    private readonly ipc = new MpvIpcClient()
    private process: ChildProcess | null = null
    private videoWindow: BrowserWindow | null = null
    private readonly observedIds = new Map<number, string>()
    private nextObserveId = 1
    private destroyed = false
    private videoRect: MpvNativeBounds | null = null
    private loggedFirstBounds = false
    private videoHidden = false
    private ownsInterpolation = false
    private readonly syncBounds = () => this.applyBounds()

    /** Raises the video window, then the UI window straight above it, so nothing can land between them. */
    private readonly raisePair = () => {
        if (this.destroyed || this.videoHidden) return
        if (this.videoWindow && !this.videoWindow.isDestroyed() && this.videoWindow.isVisible()) {
            this.videoWindow.moveTop()
        }
        if (!this.uiWindow.isDestroyed()) this.uiWindow.moveTop()
    }

    private readonly handleUiShown = () => {
        if (this.destroyed || this.videoHidden) return
        if (this.videoWindow && !this.videoWindow.isDestroyed()) this.videoWindow.showInactive()
        this.raisePair()
    }

    /** The video window is independent, so it has to follow the UI window into the tray or the taskbar. */
    private readonly handleUiHidden = () => {
        if (this.destroyed) return
        if (this.videoWindow && !this.videoWindow.isDestroyed()) this.videoWindow.hide()
    }

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
            // Never activatable and never listed: the UI window stays the app's taskbar/alt-tab entry, and
            // this one can never be raised above it by a click, alt-tab or a taskbar activation.
            focusable: false,
            skipTaskbar: true,
            webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true },
        })
        this.videoWindow.setMenu(null)

        const pipePath = `\\\\.\\pipe\\seanime-mpv-${process.pid}-${this.playerId.replace(/[^a-zA-Z0-9_-]/g, "")}`
        const args = this.buildArgs(pipePath, options)
        // Full argv, so "was --display-fps-override actually passed?" is answerable from the log alone
        log.info(`[mpv-native] starting ${binary} for ${this.playerId} :: ${args.join(" ")}`)

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
        // The child's own exit is not the only way a session dies: if the pipe drops while mpv keeps running,
        // nothing else here notices and every later command just rejects with "not connected" forever.
        // mpv runs with --no-terminal, so its own diagnosis of a problem (which adapter the GPU context
        // picked, why display-sync was refused or dropped, audio device fallbacks) went nowhere. Route it
        // into the Denshi log; the filter below keeps it to the subsystems worth reading.
        try {
            await this.ipc.command(["request_log_messages", "v"])
        }
        catch (error) {
            log.warn(`[mpv-native] could not enable mpv log messages: ${String(error)}`)
        }
        this.ipc.onClose(() => {
            if (this.destroyed) return
            log.warn(`[mpv-native] ipc closed while the session was live (${this.playerId})`)
            this.onExit("mpv ipc connection closed")
        })

        // Both windows stay top-level. Making the UI window a child of this one costs the app its taskbar and
        // alt-tab entry (Windows never lists owned windows) and still did not reliably keep it on top, so the
        // two are kept adjacent in the z-order by raising them together instead.
        // Shown immediately, even though no rect has arrived yet: mpv initialises its VO against this window,
        // and a hidden one leaves it without a display to measure (display-fps reads 0, which silently
        // disables display-sync). It sits behind the opaque UI window until the player opens, so the
        // full-size black rectangle this briefly is costs nothing visually.
        this.videoWindow.showInactive()
        this.raisePair()

        // A ResizeObserver in the renderer only reports element changes, so window moves are tracked here
        this.uiWindow.on("move", this.syncBounds)
        this.uiWindow.on("resize", this.syncBounds)
        this.uiWindow.on("enter-full-screen", this.syncBounds)
        this.uiWindow.on("leave-full-screen", this.syncBounds)

        // Keep the pair glued: z-order on every activation, visibility on minimize/tray
        this.uiWindow.on("focus", this.raisePair)
        this.uiWindow.on("show", this.handleUiShown)
        this.uiWindow.on("restore", this.handleUiShown)
        this.uiWindow.on("hide", this.handleUiHidden)
        this.uiWindow.on("minimize", this.handleUiHidden)

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

        // No --display-fps-override: mpv measures the true refresh rate itself even when embedded through
        // --wid (measured: 143.988 Hz on a panel Electron reports as 144, matching standalone mpv exactly).
        // Overriding it forced an integer, and correcting that from mpv's own estimate mid-startup fed a
        // reading taken during shader compilation back in as fact, which wrecked display sync for the
        // session. Letting mpv detect it is both simpler and what standalone mpv does.

        for (const file of options.configFiles ?? []) {
            if (fs.existsSync(file)) args.push(`--include=${file}`)
        }

        this.ownsInterpolation = !!options.options && "interpolation" in options.options
        for (const [name, value] of Object.entries(options.options ?? {})) {
            if (FORBIDDEN_OPTIONS.has(name)) {
                log.warn(`[mpv-native] ignoring forbidden option ${name}`)
                continue
            }
            args.push(`--${name}=${String(value)}`)
        }

        return args
    }

    /**
     * Interpolation makes mpv render a fresh frame every vsync instead of every source frame - at 4K on a
     * 144 Hz panel that is ~6x the output-stage work, and with user shaders (Anime4K) it is the difference
     * between zero dropped frames and hundreds. It only buys anything when the display/video ratio is far
     * from a whole number (24 fps on 60 Hz), so it is enabled for exactly that case.
     */
    private async tuneInterpolation(): Promise<void> {
        // Untouched when the user's own mpv config sets it: their choice wins.
        if (this.destroyed || !this.ownsInterpolation) return
        try {
            const displayFps = await this.getProperty("display-fps")
            const videoFps = await this.getProperty("container-fps")
            if (typeof displayFps !== "number" || typeof videoFps !== "number") return
            if (!(displayFps > 0) || !(videoFps > 0)) return
            const ratio = displayFps / videoFps
            const wanted = Math.abs(ratio - Math.round(ratio)) > INTERPOLATION_RATIO_TOLERANCE
            await this.setProperty("interpolation", wanted)
            log.info(`[mpv-native] interpolation ${wanted ? "on" : "off"}: `
                + `${videoFps.toFixed(3)} fps on ${displayFps.toFixed(3)} Hz = ratio ${ratio.toFixed(3)}`)
        }
        catch (error) {
            log.warn(`[mpv-native] could not tune interpolation: ${String(error)}`)
        }
    }

    private handleEvent(event: MpvIpcEvent): void {
        if (event.event === "log-message") {
            this.forwardLogMessage(event as unknown as MpvLogMessage)
            return
        }
        if (event.event === "file-loaded") void this.tuneInterpolation()
        if (event.event === "property-change" && typeof event.id === "number") {
            // mpv echoes the observe id; restore the name so the renderer sees prism-shaped property events
            const name = this.observedIds.get(event.id)
            if (name) event.name = name
        }
        this.onEvent(event)
    }

    /** Everything mpv reports as a problem, plus verbose output from the subsystems that explain playback. */
    private forwardLogMessage(message: MpvLogMessage): void {
        const text = String(message.text ?? "").trimEnd()
        if (!text) return
        const prefix = String(message.prefix ?? "mpv")
        const level = String(message.level ?? "info")
        if (level === "error" || level === "fatal") {
            log.error(`[mpv-native] ${prefix}: ${text}`)
            return
        }
        if (level === "warn") {
            log.warn(`[mpv-native] ${prefix}: ${text}`)
            return
        }
        if (!LOGGED_MPV_PREFIXES.some(candidate => prefix === candidate || prefix.startsWith(`${candidate}/`))) return
        log.info(`[mpv-native] ${prefix}: ${text}`)
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
        // The renderer reports CSS pixels; Electron bounds are DIP. They coincide only at zoom factor 1, so
        // a user who has ever pressed Ctrl+= would otherwise get a permanently offset, mis-sized video window.
        const zoom = this.uiWindow.webContents.getZoomFactor() || 1
        if (!this.loggedFirstBounds) {
            this.loggedFirstBounds = true
            log.info(`[mpv-native] first video rect ${JSON.stringify(rect)} content ${JSON.stringify(content)}`)
        }
        this.videoWindow.setBounds({
            x: Math.round(content.x + rect.x * zoom),
            y: Math.round(content.y + rect.y * zoom),
            width: Math.max(2, Math.round(rect.width * zoom)),
            height: Math.max(2, Math.round(rect.height * zoom)),
        })
        // The window is created at the UI window's full size and must not be shown at that size: with a warm
        // player it would sit there, black and full-screen, from app start until the first rect arrives.
        if (!this.videoHidden && !this.videoWindow.isVisible()) {
            this.videoWindow.showInactive()
            this.raisePair()
        }
    }

    setVisible(visible: boolean): void {
        this.videoHidden = !visible
        if (!this.videoWindow || this.videoWindow.isDestroyed()) return
        if (visible) {
            this.videoWindow.showInactive()
            this.raisePair()
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

        this.releaseWindows()
    }

    /**
     * Synchronous teardown for app quit. `destroy()` waits up to QUIT_GRACE_MS for mpv to exit on its own,
     * but shutdown force-exits Electron after 500 ms, so the graceful path loses the race and leaves an
     * orphan mpv.exe holding the audio device. At quit there is nothing to shut down gracefully.
     */
    killNow(): void {
        if (this.destroyed) return
        this.destroyed = true
        this.ipc.dispose()
        const child = this.process
        this.process = null
        try {
            child?.kill()
        }
        catch {
            // already gone
        }
        this.releaseWindows()
    }

    private releaseWindows(): void {
        if (!this.uiWindow.isDestroyed()) {
            this.uiWindow.off("move", this.syncBounds)
            this.uiWindow.off("resize", this.syncBounds)
            this.uiWindow.off("enter-full-screen", this.syncBounds)
            this.uiWindow.off("leave-full-screen", this.syncBounds)
            this.uiWindow.off("focus", this.raisePair)
            this.uiWindow.off("show", this.handleUiShown)
            this.uiWindow.off("restore", this.handleUiShown)
            this.uiWindow.off("hide", this.handleUiHidden)
            this.uiWindow.off("minimize", this.handleUiHidden)
        }
        if (this.videoWindow && !this.videoWindow.isDestroyed()) this.videoWindow.destroy()
        this.videoWindow = null
    }
}
