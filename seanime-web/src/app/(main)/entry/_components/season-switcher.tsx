import { Anime_GroupedEntry } from "@/api/generated/types"
import { useGetAnimeFranchise } from "@/api/hooks/anime_franchise.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { __entry_mergedSeasonAtom } from "@/app/(main)/entry/_components/merged-season-section"
import { cn } from "@/components/ui/core/styling"
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/dropdown-menu"
import { useRouter, useSearchParams } from "@/lib/navigation"
import { useAtom } from "jotai/react"
import React from "react"
import { LuChevronDown, LuListOrdered } from "react-icons/lu"

// Stremio-style season switcher. One chip per season number; split-cour seasons
// (multiple AniList entries sharing a TMDB season number) collapse into a single
// "Season N" chip that opens a merged continuous episode list. Single-cour seasons
// navigate to their entry. Rendered only when "Group seasons" is enabled.

function entryTitle(e: Anime_GroupedEntry) {
    return e.media?.title?.userPreferred || e.media?.title?.romaji || e.media?.title?.english || `#${e.mediaId}`
}

export function SeasonSwitcher({ mediaId }: { mediaId: string | number | null | undefined }) {
    const serverStatus = useServerStatus()
    const router = useRouter()
    const searchParams = useSearchParams()
    const [mergedSeason, setMergedSeason] = useAtom(__entry_mergedSeasonAtom)

    const groupSeasons = !!serverStatus?.settings?.library?.groupSeasons
    const { data: franchise } = useGetAnimeFranchise(mediaId, groupSeasons)

    const currentId = Number(mediaId)
    const single = searchParams.get("single") // suppresses auto-merge (opened a specific cour to play)

    // Group seasons into distinct (TMDB id, season) buckets, preserving order. Cours of
    // one season share a TMDB id; a sibling mislabeled with the same season number but a
    // different/empty TMDB (e.g. an unreleased next season) becomes its own bucket.
    const { order, groups } = React.useMemo(() => {
        const order: string[] = []
        const groups = new Map<string, Anime_GroupedEntry[]>()
        for (const s of (franchise?.seasons ?? [])) {
            const key = s.tmdbId ? `${s.tmdbId}:${s.seasonNumber}` : `id:${s.mediaId}`
            if (!groups.has(key)) {
                groups.set(key, [])
                order.push(key)
            }
            groups.get(key)!.push(s)
        }
        return { order, groups }
    }, [franchise?.seasons])

    // Auto-select the merged view when landing on a cour of a multi-cour season.
    React.useEffect(() => {
        if (single || mergedSeason != null) return
        for (const key of order) {
            const cours = groups.get(key)!
            if (cours.length > 1 && cours.some(c => c.mediaId === currentId)) {
                setMergedSeason({ season: cours[0].seasonNumber, tmdb: cours[0].tmdbId })
                return
            }
        }
    }, [order, groups, currentId, single, mergedSeason, setMergedSeason])

    if (!groupSeasons || !franchise) return null

    const seasons = franchise.seasons ?? []
    const extras = franchise.extras ?? []
    const watchOrder = franchise.watchOrder ?? []
    if (seasons.length + extras.length <= 1) return null

    const go = (id: number) => {
        if (id && id !== currentId) router.push(`/entry?id=${id}`)
    }

    const selectSeason = (key: string) => {
        const cours = groups.get(key) ?? []
        if (cours.length > 1) {
            setMergedSeason({ season: cours[0].seasonNumber, tmdb: cours[0].tmdbId })
        } else if (cours[0]) {
            setMergedSeason(null)
            go(cours[0].mediaId)
        }
    }

    return (
        <div className="px-4 md:px-8 mb-4 flex flex-wrap items-center gap-2" data-season-switcher data-mode="seasons">
            {order.map((key, idx) => {
                const cours = groups.get(key)!
                const isMerged = cours.length > 1
                const isCurrent = mergedSeason != null
                    ? (isMerged && mergedSeason.season === cours[0].seasonNumber && mergedSeason.tmdb === cours[0].tmdbId)
                    : cours.some(c => c.mediaId === currentId)
                return (
                    <button
                        key={key}
                        data-current={isCurrent}
                        onClick={() => selectSeason(key)}
                        title={cours.map(entryTitle).join(" + ")}
                        className={cn(
                            "px-3 py-1.5 rounded-lg border border-gray-800 text-sm hover:bg-gray-800 transition",
                            isCurrent && "bg-gray-800 border-transparent font-semibold",
                        )}
                    >
                        {`Season ${idx + 1}`}
                        {isMerged && <span className="ml-1.5 opacity-50 text-xs">{cours.length} cours</span>}
                    </button>
                )
            })}

            {watchOrder.length > 1 && (
                <DropdownMenu
                    trigger={
                        <button
                            data-season-switcher-watchorder-trigger
                            className="px-3 py-1.5 rounded-lg border border-gray-800 text-sm hover:bg-gray-800 transition flex items-center gap-1.5"
                        >
                            <LuListOrdered className="opacity-70" />
                            Watch order
                            <LuChevronDown className="opacity-70" />
                        </button>
                    }
                >
                    {watchOrder.map((e, i) => (
                        <DropdownMenuItem
                            key={e.mediaId}
                            onClick={() => go(e.mediaId)}
                            className={cn(
                                "cursor-pointer gap-2",
                                e.mediaId === currentId && "bg-gray-800 font-semibold",
                            )}
                        >
                            <span className="opacity-50 tabular-nums w-5">{i + 1}.</span>
                            <span className="line-clamp-1 flex-1">{entryTitle(e)}</span>
                            {e.isExtra && <span className="ml-2 text-xs opacity-50 uppercase">extra</span>}
                        </DropdownMenuItem>
                    ))}
                </DropdownMenu>
            )}

            {extras.length > 0 && (
                <span className="ml-1 text-xs opacity-50">+{extras.length} movie/OVA</span>
            )}
        </div>
    )
}
