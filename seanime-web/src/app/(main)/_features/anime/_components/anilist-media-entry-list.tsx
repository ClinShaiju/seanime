import { AL_AnimeCollection_MediaListCollection_Lists, AL_AnimeCollection_MediaListCollection_Lists_Entries } from "@/api/generated/types"
import { GroupedAnilistEntry, useGroupedAnilistEntries } from "@/app/(main)/_features/anime-library/_lib/group-seasons"
import { MediaCardLazyGrid } from "@/app/(main)/_features/media/_components/media-card-grid"
import { MediaEntryCard } from "@/app/(main)/_features/media/_components/media-entry-card"
import { Carousel, CarouselContent, CarouselDotButtons } from "@/components/ui/carousel"
import { cn } from "@/components/ui/core/styling"
import React from "react"

function seasonsOverlay(entry: GroupedAnilistEntry) {
    const n = entry.__franchiseSeasons ?? 0
    if (n <= 1) return undefined
    return (
        <p className="font-semibold text-white bg-gray-950 z-[5] absolute left-0 top-0 w-fit px-3 py-1 !bg-opacity-90 text-sm rounded-none rounded-br-lg">
            {n} seasons
        </p>
    )
}

// For a collapsed card, sum watched + episode count across all member seasons so
// the badge reads total-watched / total-episodes instead of just the rep season's.
function franchiseTotals(entry: GroupedAnilistEntry) {
    const members = entry.__franchiseMembers
    if (!members || members.length <= 1) return null
    let progress = 0, episodes = 0
    for (const m of members) {
        progress += m.progress ?? 0
        episodes += m.media?.episodes ?? 0
    }
    return { progress, episodes }
}


type AnilistAnimeEntryListProps = {
    list: AL_AnimeCollection_MediaListCollection_Lists | undefined
    type: "anime" | "manga"
    layout?: "grid" | "carousel"
}

/**
 * Displays a list of media entry card from an Anilist media list collection.
 */
export function AnilistAnimeEntryList(props: AnilistAnimeEntryListProps) {

    const {
        list,
        type,
        layout = "grid",
        ...rest
    } = props

    const entries = useGroupedAnilistEntries(list?.entries?.filter(Boolean) ?? [], type === "anime")

    function getListData(entry: AL_AnimeCollection_MediaListCollection_Lists_Entries) {
        return {
            progress: entry.progress!,
            score: entry.score!,
            status: entry.status!,
            startedAt: entry.startedAt?.year ? new Date(entry.startedAt.year,
                (entry.startedAt.month || 1) - 1,
                entry.startedAt.day || 1).toISOString() : undefined,
            completedAt: entry.completedAt?.year ? new Date(entry.completedAt.year,
                (entry.completedAt.month || 1) - 1,
                entry.completedAt.day || 1).toISOString() : undefined,
        }
    }

    if (layout === "carousel") return (
        <Carousel
            className={cn("w-full max-w-full !mt-0")}
            gap="xl"
            opts={{
                align: "start",
                dragFree: true,
            }}
            autoScroll={false}
        >
            <CarouselDotButtons className="-top-2" />
            <CarouselContent className="px-6">
                {entries.map(entry => {
                    const totals = franchiseTotals(entry)
                    return <div
                        key={entry.media?.id}
                        className={"relative basis-[200px] col-span-1 place-content-stretch flex-none md:basis-[250px] mx-2 mt-8 mb-0"}
                    >
                        <MediaEntryCard
                            key={`${entry.media?.id}`}
                            listData={totals ? { ...getListData(entry), progress: totals.progress } : getListData(entry)}
                            showLibraryBadge
                            media={totals ? { ...entry.media!, episodes: totals.episodes } : entry.media!}
                            showListDataButton
                            type={type}
                            overlay={seasonsOverlay(entry)}
                        />
                    </div>
                })}
            </CarouselContent>
        </Carousel>
    )

    return (
        <MediaCardLazyGrid itemCount={entries.length || 0} data-anilist-anime-entry-list>
            {entries.map((entry) => {
                const totals = franchiseTotals(entry)
                return (
                    <MediaEntryCard
                        key={`${entry.media?.id}`}
                        listData={totals ? { ...getListData(entry), progress: totals.progress } : getListData(entry)}
                        showLibraryBadge
                        media={totals ? { ...entry.media!, episodes: totals.episodes } : entry.media!}
                        showListDataButton
                        type={type}
                        overlay={seasonsOverlay(entry)}
                    />
                )
            })}
        </MediaCardLazyGrid>
    )
}
