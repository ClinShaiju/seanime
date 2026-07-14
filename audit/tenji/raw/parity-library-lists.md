# Parity audit — Library + My lists (Tenji vs seanime-web/Denshi)

Scope: seanime-web library home screen(s) + sort/filter engine (`filtering.ts`) +
continue-watching rows, and the My-Lists (AniList collection lists) page incl.
Stats tab, compared against Tenji's Library tab, Manga tab, and My-Lists screen.

## Baseline items re-verified (already ported per prior v0.1.24 batch)

1. **Up next / durable last-watched sort.**
   - web: `seanime-web/src/lib/helpers/filtering.ts` — `UP_NEXT_DESC` in
     `CONTINUE_WATCHING_SORTING_OPTIONS`, `getUpNextBoostDate()`, `upNextSortKey()`,
     `sortContinueWatchingEntries()`.
   - tenji: `src/lib/utils/filtering.ts` — near-identical `getUpNextBoostDate()`,
     `upNextSortKey()`, `sortContinueWatchingEpisodes()`, same `UP_NEXT_DESC` option.
   - Verdict: ported, logic matches almost line for line.

2. **Continue-watching rows.**
   - web: `_features/anime-library/_containers/continue-watching-header.tsx` (desktop
     hero carousel) reads `continueWatchingDefaultSorting` theme setting.
   - tenji: `src/components/features/anime/continue-watching.tsx` +
     `src/hooks/use-anime-library-collection.ts` — builds `continueWatchingList` via
     `sortContinueWatchingEpisodes()`, reads the same theme setting via
     `getThemeSetting()`. `src/lib/theme-settings.ts` mirrors the same default
     (`continueWatchingDefaultSorting: "AIRDATE_DESC"`).
   - Verdict: ported. Neither client exposes a per-screen sort *selector* for this row
     (both are theme-setting-driven only), so no gap there specifically.

3. **General collection sort modes** (`filtering.ts` `COLLECTION_SORTING_OPTIONS`,
   `ANIME_COLLECTION_SORTING_OPTIONS`, `MANGA_COLLECTION_SORTING_OPTIONS`).
   - tenji's `src/lib/utils/filtering.ts` mirrors the option sets and filter functions
     (`filterListEntries`, `filterEntriesByTitle`) used by `my-lists.tsx`.
   - Verdict: ported.

4. **My-Lists AniList tags filter.**
   - web: `lists/_containers/anilist-collection-lists.tsx` `SearchOptions` — Tags
     Combobox (multi-select, filtered by adult flag).
   - tenji: `app/(app)/(tabs)/(profile)/my-lists.tsx` (tag frequency map, capped at 60
     options) + `src/components/features/my-lists/collection-filter-sheet.tsx`
     (Tags `MultiToggle`).
   - Verdict: ported.

## Known-deferred item re-confirmed still missing/applicable

5. **My-Lists Stats tab (AniList charts).**
   - web: `lists/_containers/anilist-stats.tsx` (796 lines) — MetricCards, Highlights,
     Score Distribution BarChart, Status DonutChart, Formats/Genres RankingGrids,
     Started-by-year AreaChart, Release-years BarChart, Top Studios RankingGrid.
     Wired in as a third `StaticTabs` tab in `anilist-collection-lists.tsx` (hidden
     only for simulated/offline users).
   - tenji: `app/(app)/(tabs)/(profile)/my-lists.tsx` `TypeToggle` only offers
     "anime"/"manga" — no stats option anywhere in the file or its imports.
   - Verdict: `deferred-known`, confirmed still missing, still applicable (a phone
     client could reasonably show a condensed stats view). Reported as
     `deferred-known`, not a new finding, per task instructions.

## New gaps found (not on the known-deferred list)

6. **Anime library "Show unwatched only" toggle (`continueWatchingOnly`).**
   - web: `_features/anime-library/_containers/library-collection.tsx` —
     `LibraryCollectionListItem` renders a `DropdownMenu` on the CURRENT
     (currently-watching) shelf header with a "Show unwatched only" / "Show all"
     item that flips `params.continueWatchingOnly`. This field is part of web's
     `CollectionParams<T>` (`filtering.ts`) for the anime case.
   - tenji: `src/lib/utils/filtering.ts` `CollectionParams` type has NO
     `continueWatchingOnly` field at all (confirmed by full read + grep — zero hits
     for `continueWatchingOnly` anywhere under `src/`/`app/`). Tenji's Library tab
     (`app/(app)/(tabs)/(library)/index.tsx`) renders fixed shelves
     (Currently-watching/Paused/Planning/Completed/Dropped) with no per-shelf
     filter control.
   - Verdict: missing.

7. **Manga library "Unread chapters only" toggle (collection-level, distinct from
   the per-entry chapter-list unread filter).**
   - web: `manga/_screens/manga-library-view.tsx` lines ~302, 309, 327, 347, 352 —
     same DropdownMenu pattern as #6 but for the manga CURRENT (currently-reading)
     shelf, toggling `params.unreadOnly` ("Unread chapters only" / "Show all" label
     text).
   - tenji: `app/(app)/(tabs)/(manga)/index.tsx` (fully read) — builds fixed shelf
     sections (Currently reading/Paused/Planning/Completed/Dropped) directly from
     `libraryCollectionList.find(item => item.type === X)?.entries`, with zero
     filtering/sorting logic — no `unreadOnly`, no sort selector, no genre selector
     anywhere in the file. The only `unreadOnly` hits in the whole tenji repo are in
     `src/components/features/manga/manga-entry-chapters-view.tsx` (a *different*,
     per-entry chapter-reading-list feature, not the library collection filter) —
     confirmed this is not a false-gap / renamed reimplementation.
   - Verdict: missing.

8. **Genre selector chips on the Library/Home screen.**
   - web: both simple and detailed home views expose a genre selector —
     `_features/anime-library/_screens/library-view.tsx` (`GenreSelector`, shown
     when `!ts.disableLibraryScreenGenreSelector && entries.length > 2`) and
     `_features/anime-library/_screens/detailed-library-view.tsx` (full
     `SearchOptions` bar incl. genre Combobox). `home-screen.tsx` toggles between
     these two ("detailed" view mode vs. the default customizable item-based
     layout that includes `LibraryView` as one of its items) — genre filtering
     exists in both modes. Manga library view has the equivalent
     (`manga-library-view.tsx` lines 155/193, via `MediaGenreSelector`).
   - tenji: `src/components/features/media/media-genre-selector.tsx` exists as a
     component but is only imported/used by `app/(app)/(tabs)/discover/index.tsx`
     (Discover tab, unrelated general-browse context) — grep confirms zero usage in
     the Library or Manga tab screens.
   - Verdict: missing (component exists but isn't wired into Library/Manga tabs).

9. **Adult-only filter switch in My-Lists filter UI.**
   - web: `lists/_containers/anilist-collection-lists.tsx` `SearchOptions` renders an
     "Adult" `Switch` controlling `params.isAdult`, only when
     `serverStatus?.settings?.anilist?.enableAdultContent` is true.
   - tenji: `src/components/features/my-lists/collection-filter-sheet.tsx` (fully
     read, 213 lines) has Sort/Format/Season/Year/Status/Genres/Tags controls but no
     switch/toggle for `isAdult` anywhere — the field exists in tenji's
     `CollectionParams` type but the user has no UI to ever set it true.
   - Verdict: missing. Low impact (niche/opt-in setting), small effort (one Switch
     row in the existing filter sheet).

## Scoping note

Web's Home screen has two coexisting library layouts: a default customizable
item-based layout (`LibraryView` is one selectable item, with its own genre chips +
per-shelf continueWatchingOnly toggle) and a "detailed" full view
(`DetailedLibraryView`, with a `SearchOptions` bar including the same filters plus
Format/Status/Tags/Season/Year and StaticTabs). Both layouts expose genre filtering
and the unwatched-only toggle, so the Tenji gaps (#6, #8) apply regardless of which
web layout is treated as "primary" — Tenji has neither.
