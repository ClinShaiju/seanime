import { Anime_Episode } from "@/api/generated/types"
import { useGetMergedSeason } from "@/api/hooks/anime_franchise.hooks"
import { EpisodeCard } from "@/app/(main)/_features/anime/_components/episode-card"
import { EpisodeListPaginatedGrid } from "@/app/(main)/entry/_components/episode-list-grid"
import { EpisodeItem } from "@/app/(main)/entry/_containers/episode-list/episode-item"
import { episodeCardCarouselItemClass } from "@/components/shared/classnames"
import { AppLayoutStack } from "@/components/ui/app-layout"
import { Carousel, CarouselContent, CarouselDotButtons, CarouselItem } from "@/components/ui/carousel"
import { LoadingSpinner } from "@/components/ui/loading-spinner"
import { useRouter } from "@/lib/navigation"
import { useThemeSettings } from "@/lib/theme/theme-hooks"
import { atom } from "jotai"
import React from "react"

// Selected merged season number on the entry page (null = normal single-entry view).
export const __entry_mergedSeasonAtom = atom<number | null>(null)

// MergedSeasonSection renders a split-cour season as one continuous episode list,
// mirroring the normal episode section (a "to watch" carousel + the full list).
// Episodes keep their source cour, so watched status is per-cour; clicking an episode
// opens its cour's entry page to play (full in-place playback is the next stage).
export function MergedSeasonSection({ rootId, seasonNumber }: { rootId: number, seasonNumber: number }) {
    const ts = useThemeSettings()
    const router = useRouter()
    const { data, isLoading } = useGetMergedSeason(rootId, seasonNumber)

    // Per-cour AniList progress, used to compute per-episode watched status.
    const courProgress = React.useMemo(() => {
        const m = new Map<number, number>()
        for (const c of (data?.cours ?? [])) m.set(c.mediaId, c.progress)
        return m
    }, [data?.cours])

    // Episodes with a continuous display number; keep cour-relative numbers underneath.
    const episodes = React.useMemo(() => {
        return (data?.episodes ?? []).map((ep, i) => ({
            ...ep,
            displayTitle: `Episode ${i + 1}`,
        })) as Anime_Episode[]
    }, [data?.episodes])

    const isEpWatched = (ep: Anime_Episode) => (courProgress.get(ep.baseAnime?.id ?? -1) ?? 0) >= ep.progressNumber
    const toWatch = episodes.filter(ep => !isEpWatched(ep))

    // Open the episode's source cour to play (interim until in-place playback is wired).
    const openCour = (ep: Anime_Episode) => {
        if (ep.baseAnime?.id) router.push(`/entry?id=${ep.baseAnime.id}`)
    }

    if (isLoading) return <div className="py-16 flex justify-center"><LoadingSpinner /></div>
    if (!episodes.length) return <p className="text-center text-[--muted] py-16">No episodes found for this season.</p>

    return (
        <AppLayoutStack spacing="lg" data-merged-season-section>
            <div className="flex items-center gap-3">
                <h3 className="m-0">Season {seasonNumber}</h3>
                <span className="text-[--muted] font-medium">{data?.totalProgress ?? 0} / {data?.totalEpisodes ?? episodes.length}</span>
                {(data?.cours?.length ?? 0) > 1 && <span className="text-xs text-[--muted]">({data?.cours?.length} cours merged)</span>}
            </div>

            {toWatch.length > 0 && (
                <Carousel className="w-full max-w-full" gap="md" opts={{ align: "start" }} data-merged-season-carousel>
                    <CarouselDotButtons />
                    <CarouselContent>
                        {toWatch.map((ep, idx) => (
                            <CarouselItem key={`${ep.baseAnime?.id}-${ep.progressNumber}-${idx}`} className={episodeCardCarouselItemClass(ts.smallerEpisodeCarouselSize)}>
                                <EpisodeCard
                                    contextType="library"
                                    episode={ep}
                                    image={ep.episodeMetadata?.image || ep.baseAnime?.bannerImage || ep.baseAnime?.coverImage?.extraLarge}
                                    topTitle={ep.episodeTitle || ep.baseAnime?.title?.userPreferred}
                                    title={ep.displayTitle}
                                    length={ep.episodeMetadata?.length}
                                    onClick={() => openCour(ep)}
                                />
                            </CarouselItem>
                        ))}
                    </CarouselContent>
                </Carousel>
            )}

            <div className="space-y-10" data-merged-season-list>
                <EpisodeListPaginatedGrid
                    length={episodes.length}
                    renderItem={(index) => (
                        <EpisodeItem
                            key={`${episodes[index].baseAnime?.id}-${episodes[index].progressNumber}-${index}`}
                            episode={episodes[index]}
                            media={episodes[index].baseAnime!}
                            isWatched={isEpWatched(episodes[index])}
                            watchedProgress={courProgress.get(episodes[index].baseAnime?.id ?? -1)}
                            onPlay={() => openCour(episodes[index])}
                        />
                    )}
                />
            </div>

            <p className="text-xs text-[--muted]">
                Merged view — clicking an episode opens its season cour to play. In-place playback is being wired next.
            </p>
        </AppLayoutStack>
    )
}
