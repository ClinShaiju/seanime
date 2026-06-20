import React from "react"

// Shared "N seasons" badge for collapsed franchise cards (season grouping).
export function seasonsOverlay(count: number | undefined): React.ReactNode {
    if (!count || count <= 1) return undefined
    return (
        <p className="font-semibold text-white bg-gray-950 z-[5] absolute left-0 top-0 w-fit px-3 py-1 !bg-opacity-90 text-sm rounded-none rounded-br-lg">
            {count} seasons
        </p>
    )
}
