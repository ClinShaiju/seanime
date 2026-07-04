import { AL_BaseAnime } from "@/api/generated/types"
import { vc_loadingMediaIdAtom, vc_loadingScreenVisibleAtom } from "@/app/(main)/_features/video-core/video-core.atoms"
import { __debridstream_stateAtom } from "@/app/(main)/entry/_containers/debrid-stream/debrid-stream-overlay"
import { GradientBackground } from "@/components/shared/gradient-background"
import { cn } from "@/components/ui/core/styling"
import { useQuery } from "@tanstack/react-query"
import { useAtomValue, useSetAtom } from "jotai"
import React from "react"
import { ImSpinner2 } from "react-icons/im"

type AniZipArtwork = { fanart?: string, logo?: string, title?: string }

// Stremio-style loading artwork: TVDB fanart + clearlogo from the keyless ani.zip
// mappings API (the same source the server already uses for episode metadata).
// Fails soft — any error just means the plain gradient loading screen.
function useAniZipArtwork(mediaId: number | null | undefined) {
    return useQuery<AniZipArtwork>({
        queryKey: ["anizip-artwork", mediaId],
        enabled: !!mediaId,
        staleTime: Infinity,
        retry: 1,
        queryFn: async () => {
            const res = await fetch(`https://api.ani.zip/mappings?anilist_id=${mediaId}`)
            if (!res.ok) throw new Error("ani.zip request failed")
            const data: any = await res.json()
            const images: Array<{ coverType?: string, url?: string }> = data?.images ?? []
            const img = (type: string) => images.find(i => i.coverType?.toLowerCase() === type)?.url
            return {
                // Only "Fanart" works as a full-screen backdrop ("Banner" is a 758x140 strip)
                fanart: img("fanart"),
                logo: img("clearlogo"),
                title: data?.titles?.en || data?.titles?.["x-jat"] || data?.titles?.ja,
            }
        },
    })
}

// Rendered inside the player's loading overlay (parent is the absolute, black,
// overflow-hidden container). Shows the show's fanart dimmed behind a slowly
// breathing clearlogo (Stremio-style) while keeping the stream status text
// ("Loading metadata...", "Loading preloaded stream...") visible underneath.
export function VideoCoreLoadingScreen({ loadingState, showArtwork, media }: {
    loadingState: string | null
    showArtwork: boolean
    media?: AL_BaseAnime
}) {
    const loadingMediaId = useAtomValue(vc_loadingMediaIdAtom)
    const mediaId = loadingMediaId || media?.id
    const { data: artwork } = useAniZipArtwork(showArtwork ? mediaId : null)

    // Absorb the debrid stream status (the floating pill's data) into this screen —
    // its message is more detailed than the coarse open steps ("Adding torrent...",
    // "Downloading torrent: 45%"), and the pill hides itself while we're mounted.
    const debridState = useAtomValue(__debridstream_stateAtom)
    const statusText = debridState?.message || loadingState
    const torrentName = debridState?.torrentName

    const setLoadingScreenVisible = useSetAtom(vc_loadingScreenVisibleAtom)
    React.useEffect(() => {
        setLoadingScreenVisible(true)
        return () => setLoadingScreenVisible(false)
    }, [])

    const backdrop = artwork?.fanart || media?.bannerImage || media?.coverImage?.extraLarge
    const [backdropLoaded, setBackdropLoaded] = React.useState(false)

    const hasArtwork = showArtwork && !!backdrop
    const title = artwork?.title || media?.title?.userPreferred

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
                        backdropLoaded && "opacity-40",
                    )}
                />
            )}
            {hasArtwork && (
                <div className="absolute inset-x-0 bottom-0 h-1/3 bg-gradient-to-t from-black/80 to-transparent" />
            )}

            {/* Previous look when no artwork is available */}
            {showArtwork && !backdrop && (
                <div className="opacity-50 absolute inset-0 z-[0] overflow-hidden" data-vc-element="loading-overlay-gradient">
                    <GradientBackground duration={10} breathingRange={5} />
                </div>
            )}

            <div className="absolute inset-0 z-[1] flex flex-col items-center justify-center gap-6 p-8">
                {(hasArtwork && artwork?.logo) ? (
                    <img
                        data-vc-element="loading-screen-logo"
                        src={artwork.logo}
                        alt=""
                        className="max-w-[60%] lg:max-w-[40%] max-h-[35%] object-contain drop-shadow-lg animate-pulse"
                        style={{ animationDuration: "3s" }}
                    />
                ) : (hasArtwork && title) ? (
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
                            {hasArtwork && <ImSpinner2 className="size-4 animate-spin flex-none" />}
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
