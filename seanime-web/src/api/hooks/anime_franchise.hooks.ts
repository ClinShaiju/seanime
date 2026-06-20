import { useServerQuery } from "@/api/client/requests"
import { API_ENDPOINTS } from "@/api/generated/endpoints"
import { Anime_FranchiseGroup, Anime_FranchiseRefEntry, Anime_MergedSeason, Nullish } from "@/api/generated/types"
import React from "react"

// useGetMergedSeason fetches a split-cour season merged into one continuous episode
// list. `tmdb` disambiguates real cours from siblings mislabeled with the same season.
export function useGetMergedSeason(id: Nullish<string | number>, season: Nullish<number>, tmdb = "", enabled = true) {
    const base = API_ENDPOINTS.ANIME_FRANCHISE.GetMergedSeason.endpoint
        .replace("{id}", String(id))
        .replace("{season}", String(season))
    return useServerQuery<Anime_MergedSeason>({
        endpoint: tmdb ? `${base}?tmdb=${encodeURIComponent(tmdb)}` : base,
        method: API_ENDPOINTS.ANIME_FRANCHISE.GetMergedSeason.methods[0],
        queryKey: [API_ENDPOINTS.ANIME_FRANCHISE.GetMergedSeason.key, String(id), String(season), tmdb],
        enabled: !!id && season != null && enabled,
        staleTime: 1000 * 60 * 10,
        gcTime: 1000 * 60 * 30,
    })
}

export function useGetAnimeFranchise(id: Nullish<string | number>, enabled = true) {
    return useServerQuery<Anime_FranchiseGroup>({
        endpoint: API_ENDPOINTS.ANIME_FRANCHISE.GetAnimeFranchise.endpoint.replace("{id}", String(id)),
        method: API_ENDPOINTS.ANIME_FRANCHISE.GetAnimeFranchise.methods[0],
        queryKey: [API_ENDPOINTS.ANIME_FRANCHISE.GetAnimeFranchise.key, String(id)],
        enabled: !!id && enabled,
        // Keep showing the previous franchise while switching seasons (it's the same
        // group), so the switcher doesn't flicker/vanish. Cached server-side too.
        placeholderData: (prev) => prev,
        staleTime: 1000 * 60 * 30,
        gcTime: 1000 * 60 * 60,
    })
}

// useGetFranchiseRefs resolves franchise grouping refs (TMDB id + season) for many
// media ids in one call. Cheap (metadata only, no relation walk) + heavily cached.
export function useGetFranchiseRefs(mediaIds: number[], enabled = true) {
    const ids = React.useMemo(() => [...mediaIds].sort((a, b) => a - b), [mediaIds])
    return useServerQuery<Array<Anime_FranchiseRefEntry>>({
        endpoint: API_ENDPOINTS.ANIME_FRANCHISE.GetFranchiseRefs.endpoint,
        method: API_ENDPOINTS.ANIME_FRANCHISE.GetFranchiseRefs.methods[0],
        data: { mediaIds: ids },
        // Cheap stable signature for the key (count + endpoints) to avoid churn.
        queryKey: [API_ENDPOINTS.ANIME_FRANCHISE.GetFranchiseRefs.key, ids.length, ids[0] ?? 0, ids[ids.length - 1] ?? 0],
        enabled: enabled && ids.length > 0,
        staleTime: 1000 * 60 * 30,
        gcTime: 1000 * 60 * 60,
    })
}
