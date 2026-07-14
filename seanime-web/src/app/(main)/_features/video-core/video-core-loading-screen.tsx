import { AL_BaseAnime } from "@/api/generated/types"
import { vc_loadingMediaIdAtom, vc_loadingScreenVisibleAtom } from "@/app/(main)/_features/video-core/video-core.atoms"
// Read the LIVE debrid-stream atom — the one handle-debrid-stream.ts actually writes. The
// identically-named atom in debrid-stream-overlay.tsx is never written (that overlay was
// unmounted by the v3.9.0 merge), so importing it here left the loading screen's detailed
// debrid message/torrent-name permanently null.
import { __debridstream_stateAtom } from "@/app/(main)/entry/_containers/torrent-stream/playback-play-pill"
import { GradientBackground } from "@/components/shared/gradient-background"
import { cn } from "@/components/ui/core/styling"
import { useServerQuery } from "@/api/client/requests"
import { useAtomValue, useSetAtom } from "jotai"
import React from "react"
import { ImSpinner2 } from "react-icons/im"

export type AniZipArtwork = { fanart?: string, logo?: string, title?: string }

// ponytail: server-side filecache (7d TTL), replaces direct ani.zip fetch
export function useAnizipArtwork(mediaId: number | null | undefined) {
    return useServerQuery<AniZipArtwork>({
        endpoint: `/api/v1/anizip-artwork/${mediaId}`,
        method: "GET",
        queryKey: ["anizip-artwork", mediaId],
        enabled: !!mediaId,
        staleTime: Infinity,
    })
}

// Prefetch the actual image files into the browser cache so they're instant
// when the loading screen mounts. Call from the anime entry page.
export function useAnizipArtworkPrefetch(mediaId: number | null | undefined) {
    const { data: artwork } = useAnizipArtwork(mediaId)

    React.useEffect(() => {
        if (!artwork) return
        for (const url of [artwork.fanart, artwork.logo]) {
            if (url) {
                const img = new Image()
                img.src = url
            }
        }
    }, [artwork])
}

export function VideoCoreLoadingScreen({ loadingState, showArtwork, media }: {
    loadingState: string | null
    showArtwork: boolean
    media?: AL_BaseAnime
}) {
    const loadingMediaId = useAtomValue(vc_loadingMediaIdAtom)
    const mediaId = loadingMediaId || media?.id
    const { data: artwork } = useAnizipArtwork(showArtwork ? mediaId : null)

    const debridState = useAtomValue(__debridstream_stateAtom)
    const debridMsg = debridState?.message || null
    const [statusText, setStatusText] = React.useState<string | null>(loadingState)
    React.useEffect(() => {
        if (debridMsg) setStatusText(debridMsg)
    }, [debridMsg])
    React.useEffect(() => {
        setStatusText(loadingState)
    }, [loadingState])
    const torrentName = debridState?.torrentName

    const setLoadingScreenVisible = useSetAtom(vc_loadingScreenVisibleAtom)
    React.useEffect(() => {
        setLoadingScreenVisible(true)
        return () => setLoadingScreenVisible(false)
    }, [])

    const backdrop = artwork?.fanart || media?.bannerImage || media?.coverImage?.extraLarge
    const [backdropLoaded, setBackdropLoaded] = React.useState(false)
    const [logoLoaded, setLogoLoaded] = React.useState(false)

    const hasArtwork = showArtwork && !!backdrop
    const title = artwork?.title || media?.title?.userPreferred

    // Gate: show artwork only when all requested images are loaded
    const artworkReady = hasArtwork && backdropLoaded && (artwork?.logo ? logoLoaded : true)

    return (
        <>
            {hasArtwork && (
                <img
                    data-vc-element="loading-screen-backdrop"
                    src={backdrop}
                    alt=""
                    onLoad={() => setBackdropLoaded(true)}
                    className={cn(
                        "absolute inset-0 w-full h-full object-cover object-center",
                        "opacity-0 transition-opacity duration-1000",
                        artworkReady && "opacity-40",
                    )}
                />
            )}
            {hasArtwork && artwork?.logo && (
                // Hidden preload — actual logo rendered below only when artworkReady
                <img src={artwork.logo} alt="" onLoad={() => setLogoLoaded(true)} className="hidden" />
            )}
            {hasArtwork && (
                <div className={cn(
                    "absolute inset-x-0 bottom-0 h-1/3 bg-gradient-to-t from-black/80 to-transparent",
                    "opacity-0 transition-opacity duration-1000",
                    artworkReady && "opacity-100",
                )} />
            )}

            {/* Gradient fallback when no artwork is available */}
            {showArtwork && !backdrop && (
                <div className="opacity-50 absolute inset-0 z-[0] overflow-hidden" data-vc-element="loading-overlay-gradient">
                    <GradientBackground duration={10} breathingRange={5} />
                </div>
            )}

            <div className="absolute inset-0 z-[1] flex flex-col items-center justify-center gap-6 p-8">
                {(artworkReady && artwork?.logo) ? (
                    <img
                        data-vc-element="loading-screen-logo"
                        src={artwork.logo}
                        alt=""
                        className={cn(
                            "max-w-[60%] lg:max-w-[40%] max-h-[35%] object-contain drop-shadow-lg animate-pulse",
                            "opacity-0 transition-opacity duration-700",
                            artworkReady && "opacity-100",
                        )}
                        style={{ animationDuration: "3s" }}
                    />
                ) : (artworkReady && title) ? (
                    <h1
                        data-vc-element="loading-screen-title"
                        className="text-2xl lg:text-4xl font-bold text-white text-center text-pretty max-w-[80%] animate-pulse [text-shadow:_0_2px_12px_rgb(0_0_0_/_60%)]"
                        style={{ animationDuration: "3s" }}
                    >
                        {title}
                    </h1>
                ) : (
                    !!statusText && <ImSpinner2 className="size-20 text-white animate-spin" />
                )}

                {!!statusText && (
                    <div className="flex flex-col items-center gap-1.5" data-vc-element="loading-screen-state">
                        <div className="flex items-center gap-3 text-white/80">
                            {(artworkReady || (showArtwork && !backdrop)) && <ImSpinner2 className="size-4 animate-spin flex-none" />}
                            <p className="text-sm lg:text-base font-medium tracking-wide text-center">{statusText}</p>
                        </div>
                        {!!torrentName && (
                            <p className="text-xs text-white/40 max-w-[70%] truncate text-center [text-shadow:_0_1px_8px_rgb(0_0_0_/_60%)]">
                                {torrentName}
                            </p>
                        )}
                    </div>
                )}
            </div>
        </>
    )
}
