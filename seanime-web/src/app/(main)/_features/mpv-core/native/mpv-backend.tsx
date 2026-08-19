import type { MpvPrismPlayer } from "@mpv-prism/core"
import type { MpvPrismVideoProps, UseMpvPrismPlayerOptions } from "@mpv-prism/react"
import { MpvPrismVideo, useMpvPrismPlayer } from "@mpv-prism/react"
import React from "react"
import { MpvNativePlayer } from "./mpv-native-player"
import { MpvNativeVideo } from "./mpv-native-video"

let nativeSupported = false
let probe: Promise<boolean> | null = null
let sessionBackend: "native" | "prism" | null = null

/**
 * Asks the main process whether the native mpv window backend is both supported and enabled. Started at import
 * so the answer is in hand long before playback (which needs a user interaction) can begin.
 */
export function primeMpvBackend(): Promise<boolean> {
    if (!probe) {
        probe = Promise.resolve(window.electron?.mpvNative?.isSupported?.() ?? false)
            .then(value => (nativeSupported = !!value))
            .catch(() => false)
    }
    return probe
}

if (typeof window !== "undefined") void primeMpvBackend()

/**
 * The backend is decided once and then frozen: it depends on a setting that only takes effect after a restart
 * (the window has to be created transparent), and freezing it keeps the hook order below stable.
 */
export function isMpvNativeBackend(): boolean {
    if (sessionBackend === null) sessionBackend = nativeSupported ? "native" : "prism"
    return sessionBackend === "native"
}

function useMpvNativePlayer(options: UseMpvPrismPlayerOptions): MpvNativePlayer | null {
    const [player, setPlayer] = React.useState<MpvNativePlayer | null>(null)
    const optionsRef = React.useRef(options)
    optionsRef.current = options
    const playerId = options.playerId

    React.useEffect(() => {
        if (!playerId) return
        const instance = new MpvNativePlayer(playerId, optionsRef.current.mpv)
        setPlayer(instance)
        return () => {
            setPlayer(null)
            void instance.destroy()
        }
    }, [playerId])

    return player
}

/**
 * Creates a player on whichever backend is active. The native player is cast to the prism type because MpvCore
 * is written against it; `MpvPlayerApi` is what actually keeps the two implementations in sync.
 */
export function useMpvPlayer(options: UseMpvPrismPlayerOptions): MpvPrismPlayer | null {
    // eslint-disable-next-line react-hooks/rules-of-hooks -- the branch is frozen for the whole session
    if (isMpvNativeBackend()) return useMpvNativePlayer(options) as unknown as MpvPrismPlayer | null
    // eslint-disable-next-line react-hooks/rules-of-hooks
    return useMpvPrismPlayer(options)
}

export const MpvVideo = React.forwardRef<HTMLDivElement, MpvPrismVideoProps>(function MpvVideo(props, ref) {
    return isMpvNativeBackend()
        ? <MpvNativeVideo ref={ref} {...props} />
        : <MpvPrismVideo ref={ref} {...props} />
})
