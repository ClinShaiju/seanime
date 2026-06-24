import { useDebridPrewarmStatus } from "@/api/hooks/debrid.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { cn } from "@/components/ui/core/styling"
import React from "react"
import { LuFlame } from "react-icons/lu"

type PrewarmBadgeProps = {
    mediaId?: number
    episodeNumber?: number
    className?: string
}

/**
 * PrewarmBadge shows a small circular fire badge when the given episode has been prewarmed (its
 * debrid stream is resolved ahead of time and will play instantly).
 *  - burnt orange = prewarmed / ready (instant play)
 *  - red = fully hot (also metadata + first-frame warmed — the tier-1 target)
 *
 * Self-contained and self-gating: returns null when the episode isn't prewarmed, or when debrid /
 * preload is off. Safe to drop into any card; the underlying status query is shared (deduped) and
 * only runs when debrid preload is enabled.
 */
export function PrewarmBadge({ mediaId, episodeNumber, className }: PrewarmBadgeProps) {
    const serverStatus = useServerStatus()
    const enabled = !!serverStatus?.debridSettings?.enabled && !!serverStatus?.debridSettings?.preloadNextStream

    const { data } = useDebridPrewarmStatus(enabled)

    const match = React.useMemo(() => {
        if (!enabled || !mediaId || !episodeNumber || !data) return undefined
        return data.find(it => it.mediaId === mediaId && it.episodeNumber === episodeNumber)
    }, [enabled, data, mediaId, episodeNumber])

    if (!match) return null

    const hot = !!match.metadata

    return (
        <div
            data-prewarm-badge
            data-prewarm-hot={hot}
            title={hot ? "Prewarmed — ready (metadata loaded)" : "Prewarmed — ready"}
            className={cn(
                "size-8 rounded-full grid place-items-center ring-2 ring-black/40 shadow-sm",
                hot ? "bg-[#940a00]" : "bg-[#c24e00]", // toned-down red = metadata-hot, orange = ready
                className,
            )}
        >
            <LuFlame className="size-5 text-white/90" />
        </div>
    )
}
