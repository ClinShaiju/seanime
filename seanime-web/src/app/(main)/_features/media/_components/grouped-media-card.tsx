import { AL_BaseAnime } from "@/api/generated/types"
import { __anilist_userAnimeListDataAtom } from "@/app/(main)/_atoms/anilist.atoms"
import { MediaEntryCard } from "@/app/(main)/_features/media/_components/media-entry-card"
import { seasonsOverlay } from "@/app/(main)/_features/media/_components/seasons-overlay"
import { useAtomValue } from "jotai/react"
import React from "react"

// GroupedMediaCard renders a (possibly collapsed) franchise card for surfaces that
// list bare media (search / discover). When collapsed it sums episode counts across
// members and watched counts from the user's AniList list data, so the badge reads
// franchise totals instead of the representative season's.
type GroupedMediaCardProps = {
    media: AL_BaseAnime & { __franchiseSeasons?: number; __franchiseMembers?: AL_BaseAnime[] }
    showLibraryBadge?: boolean
    showTrailer?: boolean
    containerClassName?: string
}

export function GroupedMediaCard({ media, ...rest }: GroupedMediaCardProps) {
    const listDataMap = useAtomValue(__anilist_userAnimeListDataAtom)
    const members = media.__franchiseMembers

    const totals = React.useMemo(() => {
        if (!members || members.length <= 1) return null
        let progress = 0, episodes = 0
        for (const m of members) {
            episodes += m?.episodes ?? 0
            progress += listDataMap[String(m?.id)]?.progress ?? 0
        }
        return { progress, episodes }
    }, [members, listDataMap])

    return (
        <MediaEntryCard
            {...rest}
            type="anime"
            media={totals ? { ...media, episodes: totals.episodes } : media}
            listData={totals ? { ...(listDataMap[String(media.id)] ?? {}), progress: totals.progress } : undefined}
            showListDataButton
            overlay={seasonsOverlay(media.__franchiseSeasons)}
        />
    )
}
