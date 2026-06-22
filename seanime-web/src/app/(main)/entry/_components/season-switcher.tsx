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

    const groupSeasons = !!serverStatus?.themeSettings?.groupSeasons
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
            // Navigate to the season's first cour so the banner/cover/metadata reflect it
            // (the merged view re-opens on arrival via the auto-open effect). No-op if we're
            // already on a cour of this season.
            if (!cours.some(c => c.mediaId === currentId)) go(cours[0].mediaId)
        } else if (cours[0]) {
            setMergedSeason(null)
            go(cours[0].mediaId)
        }
    }

    // Build one ordered list (watch order) of selectable items: each season (cours
    // collapsed into a single entry) plus the tagged extras.
    type Item = { label: string, sub?: string, tag?: string, isCurrent: boolean, onSelect: () => void }
    const items: Item[] = []
    const seenKey = new Set<string>()
    for (const e of watchOrder) {
        if (e.isExtra) {
            items.push({
                label: entryTitle(e),
                tag: e.tag || undefined,
                isCurrent: mergedSeason == null && e.mediaId === currentId,
                onSelect: () => { setMergedSeason(null); go(e.mediaId) },
            })
            continue
        }
        const key = e.tmdbId ? `${e.tmdbId}:${e.seasonNumber}` : `id:${e.mediaId}`
        if (seenKey.has(key)) continue
        seenKey.add(key)
        const cours = groups.get(key) ?? [e]
        const isMerged = cours.length > 1
        items.push({
            label: `Season ${order.indexOf(key) + 1}`,
            sub: isMerged ? `${cours.length} cours` : undefined,
            isCurrent: mergedSeason != null
                ? (isMerged && mergedSeason.season === cours[0].seasonNumber && mergedSeason.tmdb === cours[0].tmdbId)
                : cours.some(c => c.mediaId === currentId),
            onSelect: () => selectSeason(key),
        })
    }
    const current = items.find(it => it.isCurrent)

    return (
        <div className="px-4 md:px-8 mb-4" data-season-switcher>
            <DropdownMenu
                align="start"
                trigger={
                    <button
                        data-season-switcher-trigger
                        className="px-3 py-1.5 rounded-lg border border-gray-800 text-sm hover:bg-gray-800 transition flex items-center gap-2"
                    >
                        <LuListOrdered className="opacity-70" />
                        <span className="opacity-60">Season:</span>
                        <span className="font-semibold line-clamp-1 max-w-[20rem]">{current?.label ?? "Select"}</span>
                        {current?.sub && <span className="opacity-50 text-xs">{current.sub}</span>}
                        <LuChevronDown className="opacity-70" />
                    </button>
                }
            >
                {items.map((it, i) => (
                    <DropdownMenuItem
                        key={i}
                        onClick={it.onSelect}
                        className={cn("cursor-pointer gap-2", it.isCurrent && "bg-gray-800 font-semibold")}
                    >
                        <span className="line-clamp-1 flex-1">{it.label}</span>
                        {it.sub && <span className="text-xs opacity-50">{it.sub}</span>}
                        {it.tag && <span className="ml-2 text-xs opacity-50 uppercase">{it.tag}</span>}
                    </DropdownMenuItem>
                ))}
            </DropdownMenu>
        </div>
    )
}
