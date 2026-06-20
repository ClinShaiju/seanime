import {
    AL_AnimeCollection_MediaListCollection_Lists_Entries,
    Anime_FranchiseRefEntry,
    Anime_LibraryCollectionEntry,
    Anime_LibraryCollectionList,
} from "@/api/generated/types"
import { useGetFranchiseRefs } from "@/api/hooks/anime_franchise.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import React from "react"

// Season-grouping for the library (Phase 2). Presentation-only overlay: collapses
// entries sharing a TMDB id into one card *within each status list* (S1 completed +
// S2 watching stay separate — that matches AniList's per-season tracking). The
// representative is the lowest-numbered season; the card carries a "N seasons" badge.

export type GroupedLibraryEntry = Anime_LibraryCollectionEntry & {
    __franchiseSeasons?: number
    __franchiseMembers?: Anime_LibraryCollectionEntry[]
}

function seasonKey(ref?: Anime_FranchiseRefEntry): number {
    const n = ref?.seasonNumber ?? -1
    return n > 0 ? n : 999 // unknown sorts last
}

// franchiseTitleKey strips trailing season/part markers from a title so sequels
// share a key. Conservative: it keeps the franchise name (no subtitle stripping),
// only used as a fallback when TMDB ids don't already group the seasons.
export function franchiseTitleKey(title?: string | null): string {
    if (!title) return ""
    const t = ` ${title.toLowerCase()} `
        .replace(/\b(the\s+)?final\s+season\b/g, " ")
        .replace(/\b\d+(st|nd|rd|th)\s+season\b/g, " ")
        .replace(/\b(second|third|fourth|fifth|sixth)\s+season\b/g, " ")
        .replace(/\bseason\s*\d+\b/g, " ")
        .replace(/\bpart\s*\d+\b/g, " ")
        .replace(/\bcour\s*\d+\b/g, " ")
        .replace(/\s(ii|iii|iv|v|vi|vii)\s/g, " ")
        .replace(/[^a-z0-9]+/g, " ")
        .trim()
    return t
}

// Formats eligible to be treated as a "season" by the title-stem fallback. OVAs,
// specials and movies are excluded so they never title-merge into the season list.
const SEASON_FORMATS = new Set(["TV", "TV_SHORT", "ONA"])

type CollapseOpts<T> = {
    repFirst?: boolean // keep the first-seen item (e.g. the airing/trending one) instead of S1
    titleOf?: (t: T) => string | undefined // enables the title-stem fallback for entries lacking a TMDB id
    formatOf?: (t: T) => string | null | undefined // AniList format; gates the title-stem fallback to TV-likes
}

// sumFranchiseProgress totals watched + episode counts across a collapsed group's
// members, so a collapsed card can show franchise totals instead of the rep season's.
export function sumFranchiseProgress<T>(
    members: T[] | undefined,
    progressOf: (m: T) => number | null | undefined,
    episodesOf: (m: T) => number | null | undefined,
): { progress: number; episodes: number } | null {
    if (!members || members.length <= 1) return null
    let progress = 0, episodes = 0
    for (const m of members) {
        progress += progressOf(m) ?? 0
        episodes += episodesOf(m) ?? 0
    }
    return { progress, episodes }
}

// collapseBy buckets items by franchise and returns one representative per bucket
// (lowest-numbered season), tagged with the member count and members. Grouping key
// is the shared TMDB id; entries without one fall back to a romaji title-stem match.
// Generic over the item type so it serves the library, AniList lists, and search.
function collapseBy<T>(
    items: T[],
    idOf: (t: T) => number | undefined,
    refMap: Map<number, Anime_FranchiseRefEntry>,
    opts: CollapseOpts<T> = {},
): Array<T & { __franchiseSeasons?: number; __franchiseMembers?: T[] }> {
    const { repFirst = false, titleOf, formatOf } = opts

    // The title-stem fallback only applies to TV-likes, so OVAs/specials/movies
    // (which often share a base title) never merge into the season list.
    const seasonEligible = (it: T): boolean => {
        if (!formatOf) return true
        return SEASON_FORMATS.has((formatOf(it) ?? "").toUpperCase())
    }

    // Map a title-stem to the TMDB group it belongs to, so a no-TMDB sequel can
    // join its earlier season's group.
    const titleKeyToGroup = new Map<string, string>()
    if (titleOf) {
        for (const it of items) {
            if (!seasonEligible(it)) continue
            const ref = refMap.get(idOf(it) ?? 0)
            if (!ref?.tmdbId) continue
            const tk = franchiseTitleKey(titleOf(it))
            if (tk && !titleKeyToGroup.has(tk)) titleKeyToGroup.set(tk, `tmdb:${ref.tmdbId}`)
        }
    }

    const keyOf = (it: T): string => {
        const id = idOf(it) ?? 0
        const ref = refMap.get(id)
        if (ref?.tmdbId) return `tmdb:${ref.tmdbId}`
        if (titleOf && seasonEligible(it)) {
            const tk = franchiseTitleKey(titleOf(it))
            if (tk) return titleKeyToGroup.get(tk) ?? `title:${tk}`
        }
        return `id:${id}`
    }

    const groups = new Map<string, T[]>()
    const order: string[] = []
    for (const it of items) {
        const key = keyOf(it)
        if (!groups.has(key)) {
            groups.set(key, [])
            order.push(key)
        }
        groups.get(key)!.push(it)
    }

    return order.map(key => {
        const members = groups.get(key)!
        if (members.length === 1) return members[0] as T & { __franchiseSeasons?: number; __franchiseMembers?: T[] }
        const rep = repFirst
            ? members[0]
            : [...members].sort((a, b) => seasonKey(refMap.get(idOf(a) ?? 0)) - seasonKey(refMap.get(idOf(b) ?? 0)))[0]
        return { ...rep, __franchiseSeasons: members.length, __franchiseMembers: members }
    })
}

function collapseEntries(
    entries: Anime_LibraryCollectionEntry[],
    refMap: Map<number, Anime_FranchiseRefEntry>,
): GroupedLibraryEntry[] {
    return collapseBy(entries, e => e.mediaId, refMap, {
        titleOf: e => e.media?.title?.romaji ?? e.media?.title?.userPreferred ?? undefined,
        formatOf: e => e.media?.format,
    })
}

function buildRefMap(refs: Anime_FranchiseRefEntry[]): Map<number, Anime_FranchiseRefEntry> {
    const m = new Map<number, Anime_FranchiseRefEntry>()
    for (const r of refs) m.set(r.mediaId, r)
    return m
}

// useGroupedCollectionList returns the collection with same-franchise entries
// collapsed when "Group seasons" is on. While refs are loading it returns the flat
// list unchanged, so the library renders immediately and collapses progressively.
export function useGroupedCollectionList(collectionList: Anime_LibraryCollectionList[]): Anime_LibraryCollectionList[] {
    const serverStatus = useServerStatus()
    const groupSeasons = !!serverStatus?.settings?.library?.groupSeasons

    const allIds = React.useMemo(
        () => groupSeasons ? collectionList.flatMap(l => l.entries ?? []).map(e => e.mediaId) : [],
        [collectionList, groupSeasons],
    )

    const { data: refs } = useGetFranchiseRefs(allIds, groupSeasons)

    return React.useMemo(() => {
        if (!groupSeasons || !refs?.length) return collectionList
        const refMap = new Map<number, Anime_FranchiseRefEntry>()
        for (const r of refs) refMap.set(r.mediaId, r)
        return collectionList.map(list => ({ ...list, entries: collapseEntries(list.entries ?? [], refMap) }))
    }, [collectionList, refs, groupSeasons])
}

// useGroupedEntries collapses a flat entry list (e.g. the detailed view's merged
// "all" view). Here collapsing is across statuses since it's a single merged list.
export function useGroupedEntries(entries: Anime_LibraryCollectionEntry[]): GroupedLibraryEntry[] {
    const serverStatus = useServerStatus()
    const groupSeasons = !!serverStatus?.settings?.library?.groupSeasons

    const allIds = React.useMemo(
        () => groupSeasons ? entries.map(e => e.mediaId) : [],
        [entries, groupSeasons],
    )

    const { data: refs } = useGetFranchiseRefs(allIds, groupSeasons)

    return React.useMemo(() => {
        if (!groupSeasons || !refs?.length) return entries
        const refMap = new Map<number, Anime_FranchiseRefEntry>()
        for (const r of refs) refMap.set(r.mediaId, r)
        return collapseEntries(entries, refMap)
    }, [entries, refs, groupSeasons])
}

// useGroupedById collapses any media list whose items expose a numeric `id`
// (e.g. search / discover results). `enabled` should be false for non-anime.
export function useGroupedById<T extends {
    id: number
    title?: { romaji?: string | null; userPreferred?: string | null } | null
    format?: string | null
}>(
    items: T[],
    enabled: boolean,
    repFirst = false,
): Array<T & { __franchiseSeasons?: number; __franchiseMembers?: T[] }> {
    const serverStatus = useServerStatus()
    const groupSeasons = enabled && !!serverStatus?.settings?.library?.groupSeasons

    const allIds = React.useMemo(
        () => groupSeasons ? items.map(m => m.id) : [],
        [items, groupSeasons],
    )

    const { data: refs } = useGetFranchiseRefs(allIds, groupSeasons)

    return React.useMemo(() => {
        if (!groupSeasons || !refs?.length) return items
        return collapseBy(items, m => m.id, buildRefMap(refs), {
            repFirst,
            titleOf: m => m.title?.romaji ?? m.title?.userPreferred ?? undefined,
            formatOf: m => m.format,
        })
    }, [items, refs, groupSeasons, repFirst])
}

export type GroupedAnilistEntry = AL_AnimeCollection_MediaListCollection_Lists_Entries & {
    __franchiseSeasons?: number
    __franchiseMembers?: AL_AnimeCollection_MediaListCollection_Lists_Entries[]
}

// useGroupedAnilistEntries collapses an AniList collection list's entries (the
// MyLists `/lists` page). `enabled` should be false for non-anime (e.g. manga).
export function useGroupedAnilistEntries(
    entries: AL_AnimeCollection_MediaListCollection_Lists_Entries[],
    enabled: boolean,
): GroupedAnilistEntry[] {
    const serverStatus = useServerStatus()
    const groupSeasons = enabled && !!serverStatus?.settings?.library?.groupSeasons

    const allIds = React.useMemo(
        () => groupSeasons ? entries.map(e => e.media?.id).filter((x): x is number => !!x) : [],
        [entries, groupSeasons],
    )

    const { data: refs } = useGetFranchiseRefs(allIds, groupSeasons)

    return React.useMemo(() => {
        if (!groupSeasons || !refs?.length) return entries
        return collapseBy(entries, e => e.media?.id, buildRefMap(refs), {
            titleOf: e => e.media?.title?.romaji ?? e.media?.title?.userPreferred ?? undefined,
            formatOf: e => e.media?.format,
        })
    }, [entries, refs, groupSeasons])
}
