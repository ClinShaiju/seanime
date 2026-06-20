import { useGroupedById } from "@/app/(main)/_features/anime-library/_lib/group-seasons"
import { GroupedMediaCard } from "@/app/(main)/_features/media/_components/grouped-media-card"
import { MediaCardLazyGrid } from "@/app/(main)/_features/media/_components/media-card-grid"
import { MediaEntryCard } from "@/app/(main)/_features/media/_components/media-entry-card"
import { useAnilistAdvancedSearch } from "@/app/(main)/search/_lib/handle-advanced-search"
import { cn } from "@/components/ui/core/styling"
import { LoadingSpinner } from "@/components/ui/loading-spinner"
import React from "react"
import { AiOutlinePlusCircle } from "react-icons/ai"

export function AdvancedSearchList() {

    const { isLoading, data, fetchNextPage, hasNextPage, type } = useAnilistAdvancedSearch()

    const rawItems = data?.pages.filter(Boolean).flatMap(n => n.Page?.media).filter(Boolean)
    const items = useGroupedById(rawItems ?? [], type === "anime")

    return <>
        {!isLoading && <MediaCardLazyGrid itemCount={items?.length ?? 0}>
            {items?.map(media => (
                type === "anime"
                    ? <GroupedMediaCard
                        key={`${media.id}`}
                        media={media as any}
                        showLibraryBadge
                        showTrailer
                    />
                    : <MediaEntryCard
                        key={`${media.id}`}
                        media={media}
                        type={type}
                    />
            ))}
        </MediaCardLazyGrid>}
        {isLoading && <LoadingSpinner />}
        {((data?.pages.filter(Boolean).flatMap(n => n.Page?.media).filter(Boolean) || []).length > 0 && hasNextPage) &&
            <div
                data-advanced-search-list-load-more-container
                className={cn(
                    "relative flex flex-col rounded-[--radius-md] animate-none",
                    "cursor-pointer border border-none text-[--muted] hover:text-white pt-24 items-center gap-2 transition",
                )}
                onClick={() => fetchNextPage()}
            >
                <AiOutlinePlusCircle className="text-4xl" />
                <p className="text-lg font-medium">Load more</p>
            </div>}
    </>
}
