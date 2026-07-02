import React from "react"

/**
 * Remembers the window scroll position for a page across in-session navigations
 * (sessionStorage), and restores it on mount once the page has grown tall enough
 * to hold it — covers async/cached content that isn't laid out on first paint.
 *
 * Global router scrollRestoration is deliberately off (upstream); this is the
 * targeted opt-in for long list pages (#826).
 */
export function useScrollRestoration(key: string) {
    React.useEffect(() => {
        const storageKey = `sea-scroll-${key}`
        const saved = Number(sessionStorage.getItem(storageKey) || 0)

        let rafId = 0
        if (saved > 0) {
            let tries = 0
            const restore = () => {
                const fits = document.documentElement.scrollHeight >= saved + window.innerHeight
                if (fits || tries > 30) {
                    window.scrollTo({ top: saved, behavior: "instant" })
                    return
                }
                tries++
                rafId = requestAnimationFrame(restore)
            }
            rafId = requestAnimationFrame(restore)
        }

        const onScroll = () => {
            sessionStorage.setItem(storageKey, String(window.scrollY))
        }
        window.addEventListener("scroll", onScroll, { passive: true })
        return () => {
            window.removeEventListener("scroll", onScroll)
            cancelAnimationFrame(rafId)
        }
    }, [key])
}
