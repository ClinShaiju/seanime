# Tenji sweep — src/atoms, src/hooks, src/lib/utils, src/lib/franchise, src/constants, jotai patterns

## Scope covered (full reads)
- src/atoms/*.ts (9 files): anilist-collection, anime-entry, download-settings, media-list, schedule, server, storage, torrent-search, websocket
- src/hooks/*.ts (6 files): use-anime-library-collection, use-dev-screen-profiler, use-ios-scroll-refresh-rate-workaround, use-manga-chapters, use-manga-library-collection, use-paginated-items
- src/lib/franchise/group-seasons.ts
- src/lib/utils/filtering.ts, logger.ts, toast.ts
- src/constants/colors.ts, images.ts
- Repo-wide jotai usage (authorized): media-entry-card.tsx, prewarm-badge.tsx, episode-card-list.tsx, episode-list-item.tsx, anime-spoilers.ts, collection.loaders.ts, anime_collection.hooks.ts (partial), unmatched.tsx (grep), app/(app)/(tabs)/(library)/index.tsx, anime-entry-screen.tsx (playback-intent consumer), anime-entry-onlinestream-section.tsx (grep), anime-entry-torrent-stream-section.tsx (grep)
- Ledger H:/Projects/seanime/tenji-audit.md read first — T1-T14 confirmed fixed, not re-reported.

## Findings reported

### A. Query-cache mutation in useAnimeLibraryCollection (src/hooks/use-anime-library-collection.ts ~L55-63)
`sortedCollection` useMemo does `currentList.entries = entries` where `currentList = data.lists.find(n => n.type === "CURRENT")` — `data` is the react-query cache object for `useGetLibraryCollection()` (fixed queryKey, shared across all subscribers: this hook, src/api/loaders/collection.loaders.ts, app/(app)/(tabs)/(profile)/unmatched.tsx). For debrid/torrent-streaming users with `data.stream` populated, this injects synthetic stream-only entries (`{media, mediaId, listData}`, missing `libraryData`/`nakamaLibraryData`) directly into the cached array. Confirmed `Anime_LibraryCollection.stream` is a real, reachable field (types.ts ~L1698). Purity violation in a useMemo; risk compounds under StrictMode double-invoke or future consumers of `.lists`.

### B. Wasted derived-state computation, never consumed (src/hooks/use-anime-library-collection.ts)
`libraryGenres` and `filteredLibraryCollectionList` (a ~28-line useMemo re-filtering the whole library keyed on `__mainLibrary_paramsAtom`) are computed on every data/params change but grep confirms zero consumers repo-wide — the only live caller, app/(app)/(tabs)/(library)/index.tsx, destructures only `{ libraryCollectionList, continueWatchingList, isLoading, refetch, hasNonLocalEpisodes }`. `__mainLibrary_paramsInputAtom` is declared and never read or written anywhere.

### C. Wide useServerStatus() subscriptions bypass memoization on hot list paths
Three components, all rendered once per visible list item:
- src/components/features/anime/prewarm-badge.tsx — `useServerStatus()` full object, needs only `debridSettings.{enabled,preloadNextStream}`. Rendered inside memoized MediaEntryCard and per-episode-card.
- src/components/features/anime/episode-card-list.tsx — `useServerStatus()` full object feeding `getEpisodeSpoilerState()`, used inside the memoized `renderEpisodeCard` callback's dep array.
- src/components/features/anime/episode-list-item.tsx (L121) — `EpisodeListItem` IS wrapped in `React.memo` (L294: `export const EpisodeListItem = React.memo(EpisodeListItemInner)`) but internally subscribes to `useServerStatus()` directly, defeating the memo. Used in 4 episode-list views (library, downloaded, server-local, online-stream).
Cross-checked src/lib/anime-spoilers.ts: `getEpisodeSpoilerState` only reads 5 `themeSettings` fields, confirming the subscription is far wider than needed. This is the same pattern T9 fixed for MediaEntryCard/serverStatusAtom (narrowed selector via useMediaCardDisplaySettings), left unaddressed in these three adjacent hot-path components.

### D. No default-merge on atomWithStorage-persisted settings atoms (lower confidence, latent)
downloadSettingsAtom, scheduleSettingsAtom, torrentSearchAcrossProvidersAtom, torrentSearchExtraProviderIdsAtom, manga provider/filter atoms — all atomWithStorage with no schema versioning/default-merge. Contrast: src/atoms/download-settings.atoms.ts's separate `getDownloadSettings()` plain-function helper DOES merge (`{...defaultDownloadSettings, ...getStoredJsonValue(...)}`), so the non-React read path is protected but the atom (React/UI) read path is not. Only manifests when a settings type gains a new field in a future release — reported at low severity/smell per evidence-discipline (no concrete current-state failure trace).

## Investigated, no finding (near-misses / rejected)
- Franchise stem regex (src/lib/franchise/group-seasons.ts `franchiseTitleKey`) — read in full, cross-checked against ledger T7/49198e6 fix (uses `\b` for roman numerals, `(season|stage)` alternation). Traced roman-numeral token regex (`\s(ii|iii|iv|v|vi|vii)\b`) for false-positive risk against real anime titles — safe due to word-boundary requirements (e.g. "vs" doesn't match bare "v"). Missing ordinals ("seventh"+ season) is a coverage gap but mirrors the deliberate short list, not a new bug, extremely rare in practice — not reported.
- `collapseBy`/`keyOf` grouping logic in group-seasons.ts — traced the titleKeyToGroup seeding loop (only tmdbId-having items seed the map, order-independent) and representative-selection sort — no bug found.
- animeEntryPlaybackIntentAtom cross-screen coupling (src/atoms/anime-entry.atoms.ts) — checked all 3 readable consumers (anime-entry-screen.tsx, anime-entry-onlinestream-section.tsx, anime-entry-torrent-stream-section.tsx): all guard consistently (mediaId check, kind check, handledPlaybackIntentRef id-dedup, safe functional `setPlaybackIntent(current => current?.id === id ? null : current)` clear). Did not find a race/overwrite path. Did not finish reading producer sites (watch-room.ts, merged-season-section.tsx, continue-watching.tsx) in full, but the consistency of the guard pattern across every consumer checked gives no reason to suspect those producers behave differently — deprioritized given time budget and lack of any lead.
- use-manga-chapters.ts `useSelectedMangaProvider` effect includes whole `serverStatus` in deps alongside destructured `defaultMangaProvider` — redundant but this hook is only used by manga-entry-screen.tsx / manga-entry-chapters-view.tsx (single-instance per screen, not a list/hot path) — not worth reporting.
- filtering.ts — O(n×m) `.find()` inside sortBy comparators (sortContinueWatchingEpisodes) — bounded by small list sizes, not flagged.
- serverStatusAtom persisting the full Status object to MMKV on every update — not flagged, MMKV writes are cheap and this is intentional persistence, not a hot-path re-render issue.
- websocket.atoms.ts parse-once hub — re-verified sound (Set-based handlers, ref-based callback freshness in useWsMessageListener).
- anilist-collection.atoms.ts narrowed per-mediaId selectors (useMediaEntryListDataValue, useAnimeLibraryEntryDataValue) — correct use of useMemo-derived atom keyed by mediaKey.
- Trivial/no-issue files: media-list.ts, storage.ts, use-dev-screen-profiler.ts (dev-only), use-ios-scroll-refresh-rate-workaround.ts (intentional no-op, commented), use-paginated-items.ts, use-manga-library-collection.ts, colors.ts, images.ts, logger.ts, toast.ts.

## Coverage note
All files explicitly listed in scope were read in full. Franchise regex and playback-intent atom (both flagged as specific concerns in the task) were investigated with concrete traces; no new bugs found beyond what's already fixed per the ledger. Did not fully read src/lib/nakama/watch-room.ts, merged-season-section.tsx, continue-watching.tsx (playback-intent producer sites) — deprioritized after 3/3 consumers showed a consistent, sound guard pattern with no lead suggesting a producer-side race.
