import { useGetMergedSeason } from "@/api/hooks/anime_franchise.hooks"
import { EpisodeCard } from "@/app/(main)/_features/anime/_components/episode-card"
import { EpisodeListGrid } from "@/app/(main)/entry/_components/episode-list-grid"
import { LoadingSpinner } from "@/components/ui/loading-spinner"
import { atom } from "jotai"
import React from "react"

// Selected merged season number on the entry page (null = normal single-entry view).
export const __entry_mergedSeasonAtom = atom<number | null>(null)

// MergedSeasonSection renders a split-cour season as one continuous episode list.
// Display-only for now: playback/progress routing per cour is the next stage.
export function MergedSeasonSection({ rootId, seasonNumber }: { rootId: number, seasonNumber: number }) {
    const { data, isLoading } = useGetMergedSeason(rootId, seasonNumber)

    const episodes = data?.episodes ?? []

    if (isLoading) return <div className="py-16 flex justify-center"><LoadingSpinner /></div>
    if (!episodes.length) return <p className="text-center text-[--muted] py-16">No episodes found for this season.</p>

    return (
        <div className="space-y-4" data-merged-season-section>
            <div className="flex items-center gap-3">
                <h3 className="m-0">Season {seasonNumber}</h3>
                <span className="text-[--muted] font-medium">{data?.totalProgress ?? 0} / {data?.totalEpisodes ?? episodes.length}</span>
                {(data?.cours?.length ?? 0) > 1 && (
                    <span className="text-xs text-[--muted]">({data?.cours?.length} cours merged)</span>
                )}
            </div>
            <EpisodeListGrid>
                {episodes.map((ep, i) => (
                    <EpisodeCard
                        key={`${ep.baseAnime?.id}-${ep.episodeNumber}-${i}`}
                        title={ep.episodeTitle || ep.displayTitle || `Episode ${i + 1}`}
                        topTitle={`Episode ${i + 1}`}
                        image={ep.episodeMetadata?.image || ep.baseAnime?.bannerImage || undefined}
                        length={ep.episodeMetadata?.length || undefined}
                        episode={ep}
                        // Playback wiring (route to the episode's source cour) comes in the next stage.
                        onClick={() => {}}
                    />
                ))}
            </EpisodeListGrid>
            <p className="text-xs text-[--muted]">
                Merged view — playback wiring per cour is in progress. Use the season chips to open an individual cour to watch for now.
            </p>
        </div>
    )
}
