# Tenji audit — sweep: `src/api/hooks/`

Scope: every `*.hooks.ts` in `H:\Projects\seanime-tenji\src\api\hooks\` (42 files, ~3929 lines).
Concerns: query key hygiene, mutation -> invalidation completeness, `enabled` gating, staleTime/gcTime
choices, optimistic-update rollback, mutation/refetch races, server state duplicated into atoms.

Excluded per task: feature-parity gaps vs web/Denshi, known deferred-parity items, by-design issues
(pause-on-lock, nakama same-account collision, EAS hermesc), anything already in `tenji-audit.md` §0
(T1-T14, all fixed as of v0.1.22), codegen style in `src/api/generated/`.

## Files read (42/42 — full coverage)

anilist.hooks.ts, anime.hooks.ts, anime_collection.hooks.ts, anime_entries.hooks.ts,
anime_franchise.hooks.ts, auth.hooks.ts, auto_downloader.hooks.ts, continuity.hooks.ts,
custom_source.hooks.ts, debrid.hooks.ts, directory_selector.hooks.ts, directstream.hooks.ts,
discord.hooks.ts, docs.hooks.ts, download.hooks.ts, explorer.hooks.ts, extensions.hooks.ts,
filecache.hooks.ts, local.hooks.ts, localfiles.hooks.ts, mal.hooks.ts, manga.hooks.ts,
manga_download.hooks.ts, mediaplayer.hooks.ts, mediastream.hooks.ts, metadata.hooks.ts,
nakama.hooks.ts, onlinestream.hooks.ts, playback_manager.hooks.ts, playlist.hooks.ts,
releases.hooks.ts, report.hooks.ts, scan.hooks.ts, scan_summary.hooks.ts, search.hooks.ts,
settings.hooks.ts, status.hooks.ts, theme.hooks.ts, torrent_client.hooks.ts,
torrent_search.hooks.ts, torrentstream.hooks.ts, user-auth.hooks.ts, videocore.hooks.ts.

Methodology: full read of every file; for each `useServerQuery`/`useServerMutation` call, checked
queryKey completeness against dynamic URL params (`endpoint.replace("{id}", ...)`), invalidation
correctness (`.key` string vs bare endpoint-config object), `enabled` gating vs queryKey params. For
every suspicious hook, grepped the exported function name across all of `src/` to determine whether it
has live call sites (excluding self-reference and the fully-commented-out generated stub file
`src/api/generated/hooks_template.ts`) — this determines dead-code vs live-bug status, which caps
severity per the evidence-discipline rule.

## Findings

### F1 — `useGetMediaMetadataParent`: queryKey missing `id`, collides across media
`src/api/hooks/metadata.hooks.ts:43-50`
```ts
export function useGetMediaMetadataParent(id: number) {
    return useServerQuery<Models_MediaMetadataParent>({
        endpoint: API_ENDPOINTS.METADATA.GetMediaMetadataParent.endpoint.replace("{id}", String(id)),
        method: API_ENDPOINTS.METADATA.GetMediaMetadataParent.methods[0],
        queryKey: [API_ENDPOINTS.METADATA.GetMediaMetadataParent.key],
        enabled: true,
    })
}
```
Endpoint is per-id but queryKey has no `id` component. If ever used by a component that stays mounted
while `id` changes (e.g. swiping between franchise entries), the second call would resolve from the
first id's cache entry instead of refetching, showing wrong-media metadata.
Status: **dead code** — zero call sites in `src/` besides self and the fully commented-out stub at
`hooks_template.ts:1823`. Capped low/smell per evidence-discipline rule (impact only on eventual wiring).

### F2 — `useGetAnimeEntrySilenceStatus`: queryKey missing `id`, collides across media
`src/api/hooks/anime_entries.hooks.ts:99-108`
```ts
export function useGetAnimeEntrySilenceStatus(id: Nullish<string | number>) {
    const { data, ...rest } = useServerQuery({
        endpoint: API_ENDPOINTS.ANIME_ENTRIES.GetAnimeEntrySilenceStatus.endpoint.replace("{id}", String(id)),
        method: API_ENDPOINTS.ANIME_ENTRIES.GetAnimeEntrySilenceStatus.methods[0],
        queryKey: [API_ENDPOINTS.ANIME_ENTRIES.GetAnimeEntrySilenceStatus.key],
        enabled: !!id,
    })

    return { isSilenced: !!data, ...rest }
}
```
Same bug shape as F1: same-id-per-mount assumption baked into `enabled: !!id` but not into `queryKey`.
Navigating from entry A to entry B without unmounting the consumer would show A's silence flag for B.
Status: **dead code** — zero call sites besides self and commented stub `hooks_template.ts:277`.
Capped low/smell.

### F3 — `useUpdateAnimeEntryRepeat`: entire cache-invalidation body commented out
`src/api/hooks/anime_entries.hooks.ts:217-231`
```ts
export function useUpdateAnimeEntryRepeat(id: Nullish<string | number>) {
    const queryClient = useQueryClient()

    return useServerMutation<boolean, UpdateAnimeEntryRepeat_Variables>({
        endpoint: API_ENDPOINTS.ANIME_ENTRIES.UpdateAnimeEntryRepeat.endpoint,
        method: API_ENDPOINTS.ANIME_ENTRIES.UpdateAnimeEntryRepeat.methods[0],
        mutationKey: [API_ENDPOINTS.ANIME_ENTRIES.UpdateAnimeEntryRepeat.key, id],
        onSuccess: async () => {
            // if (id) {
            //     await queryClient.invalidateQueries({ queryKey: [API_ENDPOINTS.ANIME_ENTRIES.GetAnimeEntry.key, String(id)] })
            // }
            // toast.success("Updated successfully")
        },
    })
}
```
If wired to a "repeat" toggle UI, a successful mutation would leave the entry's cached repeat-count
stale (no invalidation, no refetch, no success toast) until something else happens to invalidate
`GetAnimeEntry`. `queryClient` is imported and captured for exactly this purpose then never used —
looks like a debugging comment-out that was never restored.
Status: **dead code** — zero call sites besides self and commented stub `hooks_template.ts:308`.
Capped low/smell.

### F4 — `useGetAutoDownloaderRule`: queryKey missing `id`, collides across rules
`src/api/hooks/auto_downloader.hooks.ts:35-42`
```ts
export function useGetAutoDownloaderRule(id: number) {
    return useServerQuery<Anime_AutoDownloaderRule>({
        endpoint: API_ENDPOINTS.AUTO_DOWNLOADER.GetAutoDownloaderRule.endpoint.replace("{id}", String(id)),
        method: API_ENDPOINTS.AUTO_DOWNLOADER.GetAutoDownloaderRule.methods[0],
        queryKey: [API_ENDPOINTS.AUTO_DOWNLOADER.GetAutoDownloaderRule.key],
        enabled: true,
    })
}
```
Same missing-dynamic-param shape as F1/F2: a per-id endpoint with a queryKey that doesn't vary by id.
An auto-downloader rule-edit screen that reuses the same hook instance across two different rule ids
(e.g. a route param change without remount) would show the wrong rule. Sibling hooks in the same file
(`useGetAutoDownloaderRulesByAnime` line 126, `useGetAutoDownloaderProfile` line 144) correctly append
`String(id)` to their queryKey — this one is the outlier.
Status: **dead code** — zero call sites besides self and commented stub `hooks_template.ts:371`.
Capped low/smell.

### F5 — `local.hooks.ts`: 4x `invalidateQueries` passed the endpoint-config object instead of `.key`
`src/api/hooks/local.hooks.ts` — lines 33, 49, 100, 101 (confirmed via grep as the only 4 occurrences
of this exact pattern in the whole `src/api/hooks` directory):
```ts
// line 33 (useLocalAddTrackedMedia onSuccess)
await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.LOCAL.LocalGetLocalStorageSize] })
// line 49 (useLocalRemoveTrackedMedia onSuccess) — identical
await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.LOCAL.LocalGetLocalStorageSize] })
// lines 100-101 (useLocalSyncAnilistData onSuccess)
await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.ANIME_ENTRIES.GetMissingEpisodes] })
await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.LOCAL.LocalGetLocalStorageSize] })
```
`API_ENDPOINTS.LOCAL.LocalGetLocalStorageSize` (and `...GetMissingEpisodes`) is the whole endpoint
config object (`{ endpoint, methods, key }`), not the `.key` string that queries are actually keyed by
(compare the correct usage at `local.hooks.ts:132`:
`queryKey: [API_ENDPOINTS.LOCAL.LocalGetLocalStorageSize.key]`). Because TanStack Query's
`invalidateQueries` does prefix matching against actual query keys, an array containing the raw config
object will never match any real query's key array — the invalidation is a silent no-op. If live, this
would mean the local-storage-size and missing-episodes displays never refresh after adding/removing
tracked media or syncing AniList data offline-side, until an unrelated refetch happens to touch them.
Status: **dead code** — grepped `useLocalAddTrackedMedia`, `useLocalRemoveTrackedMedia`,
`useLocalSyncAnilistData` across `src/` (also broadly grepped for "offline"/"LocalGetTrackedMediaItems"/
"LocalGetSyncQueueState" across 58 files) — none of `src/lib/offline/*` or any screen imports these
`LOCAL.*` hooks; they appear superseded by the newer offline-sync mechanism in `src/lib/offline/`.
Capped low/smell.

## Near-misses / rejected candidates (not reported)

- **`extensions.hooks.ts` — WS-push-invalidation pattern.** Several mutations (e.g.
  `useReloadExternalExtension`, and others across the file) explicitly skip or partially skip query
  invalidation with comments like `// DEVNOTE: No need to refetch, the websocket listener will do it`.
  This is a deliberate architecture decision relying on `websocket-event-router.ts`, which is **out of
  this scope** (a different sweep agent's assigned area). Not reporting as a finding since I cannot
  verify from `src/api/hooks/` alone whether the WS router actually covers every case; flagging here so
  the WS-router sweep agent can cross-check coverage.
  One specific instance worth a second look by that agent: `useReloadExternalExtension`
  (`extensions.hooks.ts:228-241`) actually *does* call two explicit `invalidateQueries` AND carries the
  "websocket will do it" comment immediately after — the comment is stale/contradictory (harmless, just
  confusing) rather than a functional bug, so not reported as a finding.

- **`useUpdateAnimeEntryProgress` (anime_entries.hooks.ts:124-215) and `useUpdateMangaProgress`
  (manga.hooks.ts:138-215).** Symmetric, well-implemented offline-aware mutations: optimistic
  `setQueryData` on a correctly-keyed `[GetAnimeEntry.key, String(targetMediaId)]` / equivalent manga
  entry, conflict guard via `createConflictGuard`/`createListDataConflictGuard`, proper queue-vs-network
  branching in both `mutate` and `mutateAsync`. No issues found; recorded so this isn't re-audited by
  mistake.

- **`anilist.hooks.ts` — `useEditAnilistListEntry` / `useDeleteAnilistListEntry`.** Same high quality
  offline-queue pattern as above (`applyOptimisticListEntryUpdate`/`applyOptimisticListEntryDelete`,
  `createListEntryConflictGuard`, correctly-parametrized `getEntryQueryKey(type, mediaId)`). No issues.

- **`nakama.hooks.ts` — many mutations with empty `onSuccess: async () => {}` and no
  `invalidateQueries`** (e.g. `useSendNakamaMessage`, `useNakamaPlayVideo`, `useNakamaCreateWatchParty`,
  room create/join/leave mutations). Consistent with the same WS-push-driven-refresh architecture noted
  above (room list refreshed via `nakama-rooms-updated` per `tenji-audit.md` §2) — not reporting since
  verifying WS coverage is out of this file-scope, and the file itself is unchanged/byte-identical to
  web per the existing audit ledger.

- **`anime_franchise.hooks.ts` — `useGetFranchiseRefs` cheap queryKey signature**
  (`ids.length, ids[0] ?? 0, ids[ids.length - 1] ?? 0`) instead of the full id list. This is explicitly
  commented as deliberate (`// Cheap stable signature for the key (count + endpoints) to avoid churn.`)
  and could theoretically collide for two different same-length id arrays sharing first/last elements,
  but no concrete failure scenario traces to a real call site with that shape today — capped out per
  evidence-discipline (would need `category=smell`/`low` and I judged it not worth a slot next to
  the 5 confirmed patterns above, since it's intentional and documented).

- **Inconsistent queryKey serialization style** (`useAnilistListAnime` passes the raw `variables` object
  vs `useAnilistListRecentAiringAnime` passes `JSON.stringify(variables)`) — both work correctly with
  TanStack Query's structural key hashing; purely a style inconsistency, not a functional bug.

- Did a final broad verification pass (`Grep` for every `endpoint.replace(` call site and its paired
  `queryKey:`) across all 42 files after the initial per-file read pass, specifically to catch any
  missed dynamic-param omissions — this second pass is what surfaced F4 (`useGetAutoDownloaderRule`),
  which was missed on the first per-file read.

## Summary

5 findings, all currently dead code (zero live call sites, verified via grep against real UI + the
generated stub file), all capped at severity=low / category=smell per the evidence-discipline rule.
They share one of two shapes: (a) per-id endpoint with a queryKey that doesn't vary by id (F1, F2, F4),
or (b) `invalidateQueries` given the raw endpoint-config object instead of `.key`, making the
invalidation a silent no-op (F5), or (c) a fully commented-out invalidation body (F3). All are landmines
for whoever wires up the corresponding UI, not live bugs today.
