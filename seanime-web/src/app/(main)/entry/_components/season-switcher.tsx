import { Anime_GroupedEntry } from "@/api/generated/types"
import { useGetAnimeFranchise } from "@/api/hooks/anime_franchise.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { cn } from "@/components/ui/core/styling"
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/dropdown-menu"
import { useRouter } from "@/lib/navigation"
import React from "react"
import { LuChevronDown, LuListOrdered } from "react-icons/lu"

// Stremio-style season switcher. Presentation-only: each item navigates to that
// season's own AniList entry page — tracking/progress stay per-entry. Rendered only
// when the user enables "Group seasons". See season-select-support.md.

function entryTitle(e: Anime_GroupedEntry) {
    return e.media?.title?.userPreferred || e.media?.title?.romaji || e.media?.title?.english || `#${e.mediaId}`
}

// Label seasons, disambiguating split-cours: two entries sharing the same TMDB
// season number (e.g. S4 Part 1 / Part 2) become "Season 4 Part 1" / "Season 4 Part 2"
// instead of two identical "Season 4" chips. (seasons[] is already season-number then
// air-date ordered, so parts come out in release order.)
function seasonLabels(seasons: Anime_GroupedEntry[]): string[] {
    const counts = new Map<number, number>()
    for (const e of seasons) {
        if (e.seasonNumber > 0) counts.set(e.seasonNumber, (counts.get(e.seasonNumber) ?? 0) + 1)
    }
    const partIdx = new Map<number, number>()
    return seasons.map((e, i) => {
        const num = e.seasonNumber > 0 ? e.seasonNumber : i + 1
        if (e.seasonNumber > 0 && (counts.get(e.seasonNumber) ?? 0) > 1) {
            const part = (partIdx.get(e.seasonNumber) ?? 0) + 1
            partIdx.set(e.seasonNumber, part)
            return `Season ${num} Part ${part}`
        }
        return `Season ${num}`
    })
}

export function SeasonSwitcher({ mediaId }: { mediaId: string | number | null | undefined }) {
    const serverStatus = useServerStatus()
    const router = useRouter()

    const groupSeasons = !!serverStatus?.settings?.library?.groupSeasons

    const { data: franchise } = useGetAnimeFranchise(mediaId, groupSeasons)

    if (!groupSeasons || !franchise) return null

    const seasons = franchise.seasons ?? []
    const extras = franchise.extras ?? []
    const watchOrder = franchise.watchOrder ?? []

    // Nothing to switch between.
    if (seasons.length + extras.length <= 1) return null

    const currentId = Number(mediaId)
    const go = (id: number) => {
        if (id && id !== currentId) router.push(`/entry?id=${id}`)
    }

    const labels = seasonLabels(seasons)

    return (
        <div className="px-4 md:px-8 mb-4 flex flex-wrap items-center gap-2" data-season-switcher data-mode="seasons">
            {seasons.map((e, i) => (
                <button
                    key={e.mediaId}
                    data-current={e.mediaId === currentId}
                    onClick={() => go(e.mediaId)}
                    title={entryTitle(e)}
                    className={cn(
                        "px-3 py-1.5 rounded-lg border border-gray-800 text-sm hover:bg-gray-800 transition",
                        e.mediaId === currentId && "bg-gray-800 border-transparent font-semibold",
                    )}
                >
                    {labels[i]}
                </button>
            ))}

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
