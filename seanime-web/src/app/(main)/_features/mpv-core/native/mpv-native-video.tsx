import type { MpvPrismVideoProps } from "@mpv-prism/react"
import React from "react"
import { MpvNativePlayer } from "./mpv-native-player"

type Rect = { x: number, y: number, width: number, height: number }

const MIN_VISIBLE_SIZE = 4

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

    React.useEffect(() => {
        const element = holeRef.current
        if (!native?.setVideoRect || !element) return

        let frame = 0
        let lastRect: Rect | null = null
        let lastVisible: boolean | null = null

        // The hole moves without resizing (drawer opening, mini-player transition, window drag), and no
        // observer covers that, so its position is sampled per frame and only sent when it actually changed.
        const tick = () => {
            frame = requestAnimationFrame(tick)
            const bounds = element.getBoundingClientRect()
            const visible = bounds.width >= MIN_VISIBLE_SIZE && bounds.height >= MIN_VISIBLE_SIZE
            if (visible !== lastVisible) {
                lastVisible = visible
                native.setVideoVisible(visible)
            }
            if (!visible) return
            const rect: Rect = { x: bounds.left, y: bounds.top, width: bounds.width, height: bounds.height }
            if (sameRect(lastRect, rect)) return
            lastRect = rect
            native.setVideoRect(rect)
        }

        frame = requestAnimationFrame(tick)
        return () => cancelAnimationFrame(frame)
    }, [native])

    // Flags the document so the app stops painting over the mpv window (see globals.css)
    React.useEffect(() => {
        if (!native) return
        document.documentElement.dataset.mpvNativeVideo = "1"
        return () => {
            delete document.documentElement.dataset.mpvNativeVideo
        }
    }, [native])

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
