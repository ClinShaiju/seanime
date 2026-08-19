import type {
    MpvPrismCommandArgument,
    MpvPrismEventListener,
    MpvPrismEventType,
    MpvPrismMpvInitOptions,
    MpvPrismNativeEvent,
    MpvPrismPlayer,
    MpvPrismPropertyValue,
    MpvPrismSeekMode,
    MpvPrismTrack,
    MpvPrismTrackKind,
    MpvPrismTrackSelection,
} from "@mpv-prism/core"

/**
 * The slice of mpv-prism's player that MpvCore actually uses. Both backends must satisfy it, so a signature
 * drift between them is a compile error rather than a runtime surprise.
 */
export type MpvPlayerApi = Pick<MpvPrismPlayer,
    | "id"
    | "paused"
    | "isPip"
    | "on"
    | "off"
    | "load"
    | "play"
    | "pause"
    | "setPaused"
    | "stop"
    | "seek"
    | "setSpeed"
    | "setVolume"
    | "setMute"
    | "selectTrack"
    | "setProperty"
    | "getProperty"
    | "observeProperty"
    | "command"
    | "runCommand"
    | "getTracks"
    | "enterPip"
    | "exitPip"
    | "awaitPresentationReady"
    | "destroy">

/** Properties the backend needs for its own typed events, regardless of what the UI asks to observe. */
const BASE_OBSERVED_PROPERTIES = [
    "time-pos",
    "duration",
    "pause",
    "speed",
    "volume",
    "mute",
    "track-list",
    "demuxer-cache-state",
    "paused-for-cache",
]

const TRACK_KIND_PROPERTY: Record<MpvPrismTrackKind, string> = {
    audio: "aid",
    subtitle: "sid",
    video: "vid",
}

type Listener = (event: any) => void

function trackSelectionValue(id: MpvPrismTrackSelection): string | number {
    if (id === null || id === undefined || id === true || id === "auto") return "auto"
    if (id === false || id === "no") return "no"
    return id as string | number
}

/**
 * Drives a real mpv process (running in its own window) over the main process' JSON IPC bridge, exposing the
 * same surface as mpv-prism's player so MpvCore does not care which backend is active.
 */
export class MpvNativePlayer implements MpvPlayerApi {
    private readonly listeners = new Map<string, Set<Listener>>()
    private readonly readyPromise: Promise<void>
    private unsubscribe: (() => void) | null = null
    private destroyed = false
    private pausedState = true
    private tracks: MpvPrismTrack[] = []

    constructor(readonly id: string, options: MpvPrismMpvInitOptions = {}) {
        this.readyPromise = this.start(options)
    }

    private get bridge() {
        const bridge = window.electron?.mpvNative
        if (!bridge) throw new Error("native mpv bridge is unavailable")
        return bridge
    }

    private async start(options: MpvPrismMpvInitOptions): Promise<void> {
        this.unsubscribe = this.bridge.onEvent(payload => {
            if (payload?.playerId === this.id) this.handleEvent(payload)
        })
        await this.bridge.create(this.id, {
            options: options.options as Record<string, string | number | boolean> | undefined,
            configFiles: options.config?.files,
            observe: [...new Set([...BASE_OBSERVED_PROPERTIES, ...(options.observe ?? [])])],
        })
        for (const shader of options.shaders ?? []) {
            await this.bridge.command(this.id, ["change-list", "glsl-shaders", "append", shader])
        }
    }

    on<Type extends MpvPrismEventType>(type: Type, listener: MpvPrismEventListener<Type>): () => void {
        let set = this.listeners.get(type)
        if (!set) {
            set = new Set()
            this.listeners.set(type, set)
        }
        set.add(listener as Listener)
        return () => this.off(type, listener)
    }

    addEventListener<Type extends MpvPrismEventType>(type: Type, listener: MpvPrismEventListener<Type>): () => void {
        return this.on(type, listener)
    }

    off<Type extends MpvPrismEventType>(type: Type, listener: MpvPrismEventListener<Type>): void {
        this.listeners.get(type)?.delete(listener as Listener)
    }

    removeEventListener<Type extends MpvPrismEventType>(type: Type, listener: MpvPrismEventListener<Type>): void {
        this.off(type, listener)
    }

    private emit(type: string, event: unknown): void {
        const set = this.listeners.get(type)
        if (!set) return
        for (const listener of [...set]) {
            try {
                listener(event)
            }
            catch (error) {
                console.error(`[mpv-native] listener for "${type}" threw`, error)
            }
        }
    }

    private handleEvent(payload: any): void {
        const nativeEvent: MpvPrismNativeEvent = {
            type: payload.event,
            name: payload.name,
            value: payload.data,
            reason: payload.reason,
            error: payload.file_error ?? payload.error,
        }
        this.emit("raw", nativeEvent)

        switch (payload.event) {
            case "property-change":
                this.handlePropertyChange(payload.name, payload.data, nativeEvent)
                break
            case "file-loaded":
                this.emit("state", { state: "file-loaded", nativeEvent })
                break
            case "playback-restart":
                this.emit("state", { state: "playback-restart", nativeEvent })
                break
            case "idle":
                this.emit("state", { state: "idle", nativeEvent })
                break
            case "end-file":
                // "quit"/"stop" are teardown, not the end of playback the UI should react to
                this.emit("ended", { reason: payload.reason, error: payload.file_error, nativeEvent })
                if (payload.reason === "error") {
                    this.emit("error", { message: String(payload.file_error ?? "playback error"), nativeEvent })
                }
                break
            case "shutdown":
                this.emit("state", { state: "shutdown", nativeEvent })
                if (payload.reason) this.emit("error", { message: String(payload.reason), nativeEvent })
                break
        }
    }

    private handlePropertyChange(name: string, value: unknown, nativeEvent: MpvPrismNativeEvent): void {
        if (!name) return
        this.emit("property", { name, value, nativeEvent })

        switch (name) {
            case "time-pos":
                this.emit("position", { position: typeof value === "number" ? value : null, nativeEvent })
                break
            case "duration":
                this.emit("duration", { duration: typeof value === "number" ? value : null, nativeEvent })
                break
            case "pause":
                this.pausedState = !!value
                this.emit("paused", { paused: this.pausedState, nativeEvent })
                break
            case "speed":
                this.emit("speed", { speed: typeof value === "number" ? value : null, nativeEvent })
                break
            case "volume":
                this.emit("volume", { volume: typeof value === "number" ? value : null, nativeEvent })
                break
            case "mute":
                this.emit("mute", { muted: !!value, nativeEvent })
                break
            case "track-list":
                this.tracks = Array.isArray(value) ? value as MpvPrismTrack[] : []
                this.emit("tracks", { tracks: this.tracks, nativeEvent })
                break
            case "demuxer-cache-state":
            case "paused-for-cache":
                this.emit("cache", { state: value, nativeEvent })
                break
        }
    }

    get paused(): boolean {
        return this.pausedState
    }

    get isPip(): boolean {
        return false
    }

    async load(uri: string): Promise<void> {
        await this.readyPromise
        await this.bridge.command(this.id, ["loadfile", uri, "replace"])
    }

    async play(): Promise<void> {
        await this.setPaused(false)
    }

    async pause(): Promise<void> {
        await this.setPaused(true)
    }

    async setPaused(paused: boolean): Promise<void> {
        await this.readyPromise
        this.pausedState = paused
        await this.bridge.setProperty(this.id, "pause", paused)
    }

    async stop(): Promise<void> {
        await this.readyPromise
        await this.bridge.command(this.id, ["stop"])
    }

    async seek(seconds: number, mode: MpvPrismSeekMode = "absolute"): Promise<void> {
        await this.readyPromise
        await this.bridge.command(this.id, ["seek", seconds, mode])
    }

    async setSpeed(speed: number): Promise<void> {
        await this.setProperty("speed", speed)
    }

    async setVolume(volume: number): Promise<void> {
        await this.setProperty("volume", volume)
    }

    async setMute(muted: boolean): Promise<void> {
        await this.setProperty("mute", muted)
    }

    async selectTrack(kind: MpvPrismTrackKind, id: MpvPrismTrackSelection): Promise<void> {
        await this.setProperty(TRACK_KIND_PROPERTY[kind], trackSelectionValue(id))
    }

    async setProperty(name: string, value: MpvPrismPropertyValue): Promise<void> {
        await this.readyPromise
        await this.bridge.setProperty(this.id, name, value)
    }

    async getProperty<Value = unknown>(name: string): Promise<Value> {
        await this.readyPromise
        return await this.bridge.getProperty(this.id, name) as Value
    }

    async observeProperty(name: string): Promise<void> {
        await this.readyPromise
        await this.bridge.observeProperty(this.id, name)
    }

    async command(args: MpvPrismCommandArgument[]): Promise<void> {
        await this.readyPromise
        await this.bridge.command(this.id, args)
    }

    async runCommand(name: string, ...args: MpvPrismCommandArgument[]): Promise<void> {
        await this.command([name, ...args])
    }

    async getTracks(): Promise<MpvPrismTrack[]> {
        const tracks = await this.getProperty<MpvPrismTrack[]>("track-list")
        this.tracks = Array.isArray(tracks) ? tracks : []
        return this.tracks
    }

    // TODO(S4): PiP becomes a small always-on-top mpv window; the prism path used the <video> element's PiP.
    async enterPip(): Promise<void> {
        throw new Error("picture-in-picture is not available on the native mpv backend yet")
    }

    async exitPip(): Promise<void> {
        // Nothing to leave: PiP is never entered on this backend
    }

    /** Resolves once the mpv process and its window exist, i.e. as soon as a load can be shown. */
    async awaitPresentationReady(): Promise<void> {
        await this.readyPromise
    }

    /** Captures a frame through mpv (there is no DOM frame to read on this backend). Returns base64 PNG. */
    async captureScreenshot(): Promise<string> {
        await this.readyPromise
        return this.bridge.screenshot(this.id)
    }

    /** Positions the mpv window under the UI's video element. Rect is in CSS px relative to the window. */
    setVideoRect(rect: { x: number, y: number, width: number, height: number }): void {
        if (this.destroyed) return
        this.bridge.setVideoRect(this.id, rect)
    }

    setVideoVisible(visible: boolean): void {
        if (this.destroyed) return
        this.bridge.setVisible(this.id, visible)
    }

    async destroy(): Promise<void> {
        if (this.destroyed) return
        this.destroyed = true
        this.unsubscribe?.()
        this.unsubscribe = null
        this.listeners.clear()
        try {
            await this.bridge.destroy(this.id)
        }
        catch (error) {
            console.error("[mpv-native] failed to destroy session", error)
        }
    }
}
