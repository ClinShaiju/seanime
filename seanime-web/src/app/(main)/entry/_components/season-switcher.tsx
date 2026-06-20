import { Anime_GroupedEntry } from "@/api/generated/types"
import { useGetAnimeFranchise } from "@/api/hooks/anime_franchise.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { __entry_mergedSeasonAtom } from "@/app/(main)/entry/_components/merged-season-section"
import { cn } from "@/components/ui/core/styling"
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/dropdown-menu"
import { useRouter } from "@/lib/navigation"
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
    const [mergedSeason, setMergedSeason] = useAtom(__entry_mergedSeasonAtom)

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

    // Collapse cours: one chip per distinct season number, in order.
    const order: number[] = []
    const coursByNum = new Map<number, Anime_GroupedEntry[]>()
    for (const s of seasons) {
        if (!coursByNum.has(s.seasonNumber)) {
            coursByNum.set(s.seasonNumber, [])
            order.push(s.seasonNumber)
        }
        coursByNum.get(s.seasonNumber)!.push(s)
    }
    const currentSeasonNum = seasons.find(s => s.mediaId === currentId)?.seasonNumber

    const selectSeason = (num: number) => {
        const cours = coursByNum.get(num) ?? []
        if (cours.length > 1) {
            setMergedSeason(num) // show merged continuous episode list in place
        } else if (cours[0]) {
            setMergedSeason(null)
            go(cours[0].mediaId)
        }
    }

    return (
        <div className="px-4 md:px-8 mb-4 flex flex-wrap items-center gap-2" data-season-switcher data-mode="seasons">
            {order.map((num, idx) => {
                const cours = coursByNum.get(num)!
                const isMerged = cours.length > 1
                const label = num > 0 ? `Season ${num}` : `Season ${idx + 1}`
                const isCurrent = mergedSeason != null
                    ? mergedSeason === num
                    : (isMerged ? currentSeasonNum === num : cours[0].mediaId === currentId)
                return (
                    <button
                        key={num}
                        data-current={isCurrent}
                        onClick={() => selectSeason(num)}
                        title={cours.map(entryTitle).join(" + ")}
                        className={cn(
                            "px-3 py-1.5 rounded-lg border border-gray-800 text-sm hover:bg-gray-800 transition",
                            isCurrent && "bg-gray-800 border-transparent font-semibold",
                        )}
                    >
                        {label}
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
