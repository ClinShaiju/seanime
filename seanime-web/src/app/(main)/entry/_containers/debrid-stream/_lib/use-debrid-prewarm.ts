import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { useHandleStartDebridStream } from "@/app/(main)/entry/_containers/debrid-stream/_lib/handle-debrid-stream"
import React from "react"

/**
 * useDebridPrewarm resolves & caches a debrid stream URL ahead of an explicit play, so playback
 * starts instantly. It reuses the existing auto-select preload path (`preload: true`).
 *
 * Guards (so it never wastes debrid quota):
 *  - debrid enabled + `preloadNextStream` setting on
 *  - the device's playback type can actually consume a preload (native / external player)
 *  - de-dupes the same target within a mount (backend also de-dupes in-flight/cached keys)
 *
 * It never influences which torrent is selected — auto-select ranking is unchanged.
 */
export function useDebridPrewarm() {
    const serverStatus = useServerStatus()
    const { handleAutoSelectStream, isPreloadablePlaybackType } = useHandleStartDebridStream()

    const enabled = !!serverStatus?.debridSettings?.enabled
        && !!serverStatus?.debridSettings?.preloadNextStream
        && isPreloadablePlaybackType

    const firedRef = React.useRef<Set<string>>(new Set())
    const timerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)

    const cancel = React.useCallback(() => {
        if (timerRef.current) {
            clearTimeout(timerRef.current)
            timerRef.current = null
        }
    }, [])

    const prewarm = React.useCallback((
        params: { mediaId?: number, episodeNumber?: number, aniDBEpisode?: string | null },
        opts?: { debounceMs?: number },
    ) => {
        if (!enabled) return
        const { mediaId, episodeNumber, aniDBEpisode } = params
        if (!mediaId || !episodeNumber || !aniDBEpisode) return

        const key = `${mediaId}|${episodeNumber}|${aniDBEpisode}`
        if (firedRef.current.has(key)) return

        cancel()
        const run = () => {
            firedRef.current.add(key)
            handleAutoSelectStream({ mediaId, episodeNumber, aniDBEpisode, preload: true })
        }
        const debounceMs = opts?.debounceMs ?? 0
        if (debounceMs > 0) {
            timerRef.current = setTimeout(run, debounceMs)
        } else {
            run()
        }
    }, [enabled, handleAutoSelectStream, cancel])

    React.useEffect(() => cancel, [cancel])

    return { prewarm, cancel, enabled }
}
