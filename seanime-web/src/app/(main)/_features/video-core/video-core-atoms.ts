// Atoms with no dependencies
import { atom } from "jotai"
import { derive } from "jotai-derive"

export const vc_menuOpen = atom<string | null>(null)
export const vc_menuSectionOpen = atom<string | null>(null)
export const vc_menuSubSectionOpen = atom<string | null>(null)

export const vc_activePlayerId = atom<string | null>(null)
export const vc_isMobile = atom(false)
export const vc_isSwiping = atom(false) // Mobile swipe state
export const vc_swipeSeekTime = atom<number | null>(null) // Mobile swipe seek time
export const vc_videoSize = atom({ width: 1, height: 1 })
export const vc_realVideoSize = atom({ width: 0, height: 0 })
export const vc_duration = atom(1)
export const vc_currentTime = atom(0)
export const vc_playbackRate = atom(1)
export const vc_readyState = atom(0)
export const vc_buffering = atom(false)
export const vc_isMuted = atom(false)
export const vc_volume = atom(1)
export const vc_subtitleDelay = atom(0)
export const vc_isFullscreen = atom(false)
export const vc_seeking = atom(false)
export const vc_seekingTargetProgress = atom(0) // 0-100
export const vc_timeRanges = atom<TimeRanges | null>(null)
export const vc_closestBufferedTime = derive([vc_timeRanges, vc_currentTime], (tr, currentTime) => {
    if (!tr) return 0
    let closest = 0
    for (let i = 0; i < tr.length; i++) {
        const start = tr.start(i)
        const end = tr.end(i)
        if (currentTime >= start && currentTime <= end) {
            return end
        }
        if (end >= currentTime && closest > end) {
            closest = end
        }
    }
    return closest
})
export const vc_ended = atom(false)
export const vc_paused = atom(true)
export const vc_miniPlayer = atom(false)

export const vc_hoveringControlBar = atom(false)

export const vc_cursorBusy = derive([vc_hoveringControlBar, vc_menuOpen], (f1, f2) => {
    return f1 || !!f2
})
export const vc_cursorPosition = atom({ x: 0, y: 0 })
export const vc_busy = atom(true)
export const vc_videoElement = atom<HTMLVideoElement | null>(null)
export const vc_containerElement = atom<HTMLDivElement | null>(null)
export const vc_previousPausedState = atom(false)
export const vc_lastKnownProgress = atom<{ mediaId: number, progressNumber: number, time: number } | null>(null)

// Global (UNSCOPED) mirrors of the active player's vc_videoElement / vc_lastKnownProgress.
// vc_videoElement above is SCOPED to VideoCoreProvider (jotai-scope), so consumers mounted OUTSIDE
// that provider — notably the app-wide watch-room sync hook — read the unscoped atom and only ever
// see null. VideoCoreProvider renders a bridge INSIDE the scope that mirrors the active player's
// values into these atoms. IMPORTANT: never add these to the ScopeProvider `atoms` list, or they'd
// be scoped too and the bridge would break.
export const vc_globalVideoElement = atom<HTMLVideoElement | null>(null)
export const vc_globalLastProgress = atom<{ mediaId: number, progressNumber: number, time: number } | null>(null)
export const vc_skipOpeningTime = atom<number | null>(null)
export const vc_skipEndingTime = atom<number | null>(null)

export const vc_globalMiniPlayerAtom = atom(false)

// PlayerSyncControl: a player-agnostic surface that watch-room sync reads instead of a raw
// HTMLVideoElement. Both the VideoCore DOM bridge and the MpvCore native player populate it.
// This lets the sync hook drive whichever player is active without reaching for either one's
// private internals (the MpvCore mpv-prism instance has no DOM element).
export type PlayerSyncControl = {
    readonly currentTime: number
    readonly paused: boolean
    readonly duration: number
    /** true while the player is buffering/seeking (not a user-initiated pause) */
    readonly seeking: boolean
    /** HTML readyState-like: >=3 means enough data, <3 means stalled. */
    readonly readyState: number
    play(): void
    pause(): void
    seek(time: number): void
    setPlaybackRate(rate: number): void
    readonly playbackRate: number
    /** The underlying DOM element (only for VideoCore — MpvCore has no DOM element). Used by
     * the watch-room sync hook to attach discrete play/pause/seeked event listeners for
     * immediate relay. */
    readonly domElement?: HTMLVideoElement
    /** Subscribe to discrete player actions (play/pause/seek). MpvCore fires these from its
     * IPC event handlers since it has no DOM element to addEventListener on. Returns an
     * unsubscribe function. */
    subscribe?(cb: (action: "play" | "pause" | "seeked") => void): () => void
}

// The active player's sync control. null when no player is mounted (the sync hook early-returns
// on null, same as it does on null videoElement today).
export const vc_globalPlayerSyncControl = atom<PlayerSyncControl | null>(null)

