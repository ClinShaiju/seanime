import type { MpvPrismVideoProps } from "@mpv-prism/react"
import { useAtomValue } from "jotai/react"
import React from "react"
import { mpvCore_stateAtom } from "../mpv-core.atoms"
import { MpvNativePlayer } from "./mpv-native-player"

type Rect = { x: number, y: number, width: number, height: number }

const MIN_VISIBLE_SIZE = 4
/** The app shell that gets clipped around the hole; must match the selector used in globals.css. */
const SHELL_SELECTOR = ".UI-AppLayout__root"
/** How long the hole must hold still before per-vsync sampling backs off. */
const SETTLE_MS = 400
/** Sampling interval once it has settled - fast enough that any movement is picked up within a frame or two. */
const IDLE_SAMPLE_MS = 100

function sameRect(a: Rect | null, b: Rect): boolean {
    return !!a && Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5
        && Math.abs(a.width - b.width) < 0.5 && Math.abs(a.height - b.height) < 0.5
}

/**
 * Stand-in for `MpvPrismVideo` on the native backend: there is no frame to paint here, because mpv draws into
 * its own window behind this one. This renders a transparent hole and keeps reporting where that hole is so
 * the main process can keep the mpv window glued to it.
 */
export const MpvNativeVideo = React.forwardRef<HTMLDivElement, MpvPrismVideoProps>(function MpvNativeVideo(props, ref) {
    const {
        player, children, videoStyle, overlayStyle, style,
        // presenter-only props that have no meaning without a frame pipeline
        fit, maxQueuedFrames, lowLatency, textureSize, flipY, presentationMode, frameTransport,
        ...rest
    } = props

    const holeRef = React.useRef<HTMLDivElement | null>(null)
    const native = player as unknown as MpvNativePlayer | null

    // The mini player floats over the app, which keeps painting behind it. A descendant cannot make an
    // ancestor see-through, so the app shell gets clipped around this rect instead (see globals.css).
    const miniPlayer = useAtomValue(mpvCore_stateAtom).miniPlayer
    const miniPlayerRef = React.useRef(miniPlayer)
    miniPlayerRef.current = miniPlayer

    React.useEffect(() => {
        const element = holeRef.current
        if (!native?.setVideoRect || !element) return

        let frame = 0
        let lastRect: Rect | null = null
        let lastVisible: boolean | null = null

        let lastSample = 0
        let stableSince = 0
        let lastMini = miniPlayerRef.current
        let lastShellX = Number.NaN
        let lastShellY = Number.NaN

        // The hole moves without resizing (drawer opening, mini-player transition, window drag), and no
        // observer covers that, so its position is sampled per frame and only sent when it actually changed.
        // Once it has held still, though, sampling every vsync is pure waste: each read forces layout and
        // keeps Chromium's compositor busy at display rate, competing with mpv for the frame time this whole
        // backend exists to protect. So it backs off while nothing moves and snaps back the moment it does.
        const tick = (now: number) => {
            frame = requestAnimationFrame(tick)
            // Entering or leaving the mini player moves the hole across the screen. Sampling must go back to
            // every frame for that, or the clip-path keeps the old rect for up to IDLE_SAMPLE_MS and the app
            // flashes see-through through a hole the video no longer fills.
            if (miniPlayerRef.current !== lastMini) {
                lastMini = miniPlayerRef.current
                stableSince = 0
            }
            if (stableSince && now - stableSince > SETTLE_MS && now - lastSample < IDLE_SAMPLE_MS) return
            lastSample = now
            const bounds = element.getBoundingClientRect()
            const visible = bounds.width >= MIN_VISIBLE_SIZE && bounds.height >= MIN_VISIBLE_SIZE
            if (visible !== lastVisible) {
                lastVisible = visible
                native.setVideoVisible(visible)
            }
            if (!visible) return
            // clip-path resolves against the clipped element's own border box, and the app shell is a normal
            // flow element, so once the page is scrolled its box no longer starts at viewport 0,0. Feeding it
            // viewport coordinates cut the hole scrollY pixels away from the video and let the page paint
            // straight over the mini player. The shell therefore gets its own shell-relative pair.
            const shell = document.querySelector(SHELL_SELECTOR)
            const origin = shell ? shell.getBoundingClientRect() : null
            const shellX = origin ? origin.left : 0
            const shellY = origin ? origin.top : 0
            const rect: Rect = { x: bounds.left, y: bounds.top, width: bounds.width, height: bounds.height }
            // Scrolling moves the shell's box without moving the (fixed) hole, so the offset has to take part
            // in the change check or the clip silently goes stale on scroll.
            if (sameRect(lastRect, rect) && shellX === lastShellX && shellY === lastShellY) {
                if (!stableSince) stableSince = now
                return
            }
            stableSince = 0
            lastRect = rect
            lastShellX = shellX
            lastShellY = shellY
            native.setVideoRect(rect)
            // Drives the clip-path hole the app shell is cut with while the mini player is up
            const style = document.documentElement.style
            style.setProperty("--mpv-hole-x1", `${bounds.left}px`)
            style.setProperty("--mpv-hole-y1", `${bounds.top}px`)
            style.setProperty("--mpv-hole-x2", `${bounds.right}px`)
            style.setProperty("--mpv-hole-y2", `${bounds.bottom}px`)
            style.setProperty("--mpv-shell-hole-x1", `${bounds.left - shellX}px`)
            style.setProperty("--mpv-shell-hole-y1", `${bounds.top - shellY}px`)
            style.setProperty("--mpv-shell-hole-x2", `${bounds.right - shellX}px`)
            style.setProperty("--mpv-shell-hole-y2", `${bounds.bottom - shellY}px`)
        }

        frame = requestAnimationFrame(tick)
        return () => {
            cancelAnimationFrame(frame)
            for (const name of ["--mpv-hole-x1", "--mpv-hole-y1", "--mpv-hole-x2", "--mpv-hole-y2",
                "--mpv-shell-hole-x1", "--mpv-shell-hole-y1", "--mpv-shell-hole-x2", "--mpv-shell-hole-y2"]) {
                document.documentElement.style.removeProperty(name)
            }
        }
    }, [native])

    // Only strip the app's own background once mpv is actually running. If it failed to start there is
    // nothing behind this window, and a transparent shell would show the user's desktop through the app with
    // no explanation of what went wrong.
    const [mpvReady, setMpvReady] = React.useState(false)
    React.useEffect(() => {
        if (!native) return
        let cancelled = false
        native.awaitPresentationReady()
            .then(() => !cancelled && setMpvReady(true))
            .catch(() => undefined)
        return () => {
            cancelled = true
            setMpvReady(false)
        }
    }, [native])

    // Flags the document so the app stops painting over the mpv window (see globals.css). Fullscreen hides
    // the app shell outright; the mini player only needs a hole cut where the video sits.
    React.useEffect(() => {
        if (!native || !mpvReady) return
        document.documentElement.dataset.mpvNativeVideo = miniPlayer ? "mini" : "fullscreen"
        return () => {
            delete document.documentElement.dataset.mpvNativeVideo
        }
    }, [native, miniPlayer, mpvReady])

    return (
        <div ref={ref} style={{ position: "relative", ...style }} {...rest}>
            <div
                ref={holeRef}
                data-mpv-native-hole=""
                style={{ position: "absolute", inset: 0, background: "transparent", ...videoStyle }}
            />
            <div style={{ position: "absolute", inset: 0, ...overlayStyle }}>
                {children}
            </div>
        </div>
    )
})
