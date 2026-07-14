# Tenji audit sweep — discover / my-lists / lib/search

Scope: `src/components/features/discover/`, `src/components/features/my-lists/`, `src/lib/search/`
Concerns called out: row virtualization, filter state, pagination, schedule-section correctness
(timezones), search debouncing, empty/error states, Aired-Recently and Missing/Upcoming sections.

## Files read (all files in scope, full read)

- src/components/features/discover/discover-hero-carousel.tsx (497 lines)
- src/components/features/discover/discover-queries.ts (130 lines)
- src/components/features/discover/search-filter-sheet.tsx (280 lines)
- src/components/features/my-lists/collection-filter-sheet.tsx (212 lines)
- src/lib/search/search-atoms.ts (108 lines)
- src/lib/search/search-constants.ts (509 lines, mostly static data tables)

Cross-referenced (outside scope, read-only for context, not audited):
- src/lib/utils/filtering.ts (CollectionParams / sorting option tables / filter+sort logic)
- src/lib/theme-settings.ts (AnimeCollectionSorting/MangaCollectionSorting types, default sorting settings)
- src/hooks/use-anime-library-collection.ts (confirms default sorting can be an anime-only value)
- src/api/hooks/anilist.hooks.ts `useAnilistListRecentAiringAnime` (confirms queryKey = JSON.stringify(variables))
- src/api/hooks/anime_entries.hooks.ts (grep only, confirmed Missing/Upcoming section lives outside scope)

Directory only contains these 6 files — no subfolders, no hooks/index files hiding extra scope.
Discover screen itself (app/ route that renders the hero carousel + rows) and the actual
Missing/Upcoming section component are NOT in this scope (live in app/ and src/api/hooks
respectively) — did not audit those, only the query-variable builders / filter sheets assigned.

## Findings

### 1. `useDiscoverRecentReleases` recomputes wall-clock query variables every call → query-key thrashing (medium, perf)
File: src/components/features/discover/discover-queries.ts:96-103
```ts
export function useDiscoverRecentReleases(enabled: boolean = true) {
    return useAnilistListRecentAiringAnime({
        page: 1,
        perPage: 50,
        airingAt_lesser: Math.floor(new Date().getTime() / 1000),
        airingAt_greater: Math.floor(subDays(new Date(), 14).getTime() / 1000),
    }, enabled)
}
```
`useAnilistListRecentAiringAnime` (src/api/hooks/anilist.hooks.ts:293-301) builds its TanStack
Query key as `[key, JSON.stringify(variables)]`. Because `airingAt_lesser`/`airingAt_greater` are
computed inline with `new Date()` on every call (not memoized), every time the component that
calls `useDiscoverRecentReleases()` re-renders in a different wall-clock second, the query key
string changes. TanStack Query treats this as a brand-new query: a fresh network request fires,
the previous cache entry is abandoned (row shows a loading/empty flash), and the old entry lingers
in the query cache until GC. Any parent re-render more than ~1s after mount (tab focus, unrelated
state change, react-query background refetch elsewhere on the screen) re-triggers this. Net effect:
the "Aired Recently" row silently refetches/flickers far more often than intended, and query cache
accumulates stale entries over a session.
Fix direction: compute the bounds once with `useMemo`/`useRef` (or round to the day) so the key is
stable for the component's lifetime / a coarser cache window.

### 2. `CollectionFilterSheet` sort picker ignores the anime/manga-specific sort options the app itself defines and applies (medium, ux/smell)
File: src/components/features/my-lists/collection-filter-sheet.tsx:16, :113-120
```ts
import { COLLECTION_SORTING_OPTIONS, CollectionParams, DEFAULT_COLLECTION_PARAMS } from "@/lib/utils/filtering"
...
<InlineSelect
    options={COLLECTION_SORTING_OPTIONS}
    value={draft.sorting}
    nullable={false}
    onSelect={v => v && setDraft(d => ({ ...d, sorting: v as CollectionParams["sorting"] }))}
/>
```
`src/lib/utils/filtering.ts` exports `ANIME_COLLECTION_SORTING_OPTIONS` (adds "Aired recently and
not up-to-date" / AIRDATE_DESC, "Most unwatched episodes" / UNWATCHED_EPISODES_DESC, "Most recent
watch" / LAST_WATCHED_DESC, etc.) and `MANGA_COLLECTION_SORTING_OPTIONS` (adds "Most unread
chapters" / UNREAD_CHAPTERS_DESC). Both are supersets of the generic `COLLECTION_SORTING_OPTIONS`
used here, and both are **never imported/used anywhere in the codebase** (confirmed via grep — the
only references are their own definitions in filtering.ts). Meanwhile `CollectionParams["sorting"]`
(the `CollectionSorting` union type) *does* include all these values, and
`filterAndSortAnimeCollectionEntries`/`filterAndSortMangaCollectionEntries` in filtering.ts contain
real branches for AIRDATE, AIRDATE_DESC, UNWATCHED_EPISODES(_DESC), UNREAD_CHAPTERS(_DESC) — fully
wired sorting logic with no UI path to reach it. Compounding: `theme-settings.ts` lets a user set
`animeLibraryCollectionDefaultSorting` to one of these anime-only values (confirmed one caller,
use-anime-library-collection.ts:19/34, defaults to `sorting: animeLibraryDefaultSorting`), so a user
who has e.g. "Most recent watch" set as their default sort will open this filter sheet and see the
`InlineSelect` unable to show any option as selected (their actual applied value isn't in
`COLLECTION_SORTING_OPTIONS`), and cannot re-select it once they touch any other sort option —
Reset falls back to `DEFAULT_COLLECTION_PARAMS.sorting = "SCORE_DESC"`, not their configured default.
This isn't a parity gap vs another client — it's dead first-party code (two exported option tables)
that was clearly built to be plugged into exactly this component and never was.

### 3. Search filter sheet: disabling "Adult Content" leaves previously-picked adult tags stuck (active, invisible, unremovable except full Reset) (medium, ux/correctness)
File: src/components/features/discover/search-filter-sheet.tsx:215-223
```tsx
<FormField label="Tags" icon="pricetag-outline">
    <MultiToggle
        options={SEARCH_MEDIA_TAGS
            .filter(tag => (draft.isAdult && serverStatus?.settings?.anilist?.enableAdultContent) ? true : !tag.isAdult)
            .map(tag => ({ value: tag.name, label: tag.name }))}
        values={draft.tags}
        onToggle={toggleTag}
    />
</FormField>
```
`MultiToggle` (src/components/shared/multi-toggle.tsx:12-41) only renders chips for the `options`
it's given — it does not render a chip for a value present in `values` but absent from `options`.
Scenario: server has `enableAdultContent: true`. User toggles "Adult Content" on, selects an
adult-only tag (e.g. "Rape", "Incest" — the majority of `SEARCH_MEDIA_TAGS` entries with
`isAdult: true`), then toggles "Adult Content" back off. The tag list re-filters to hide all
`isAdult` tags, so the selected tag's chip disappears entirely — there is no way to deselect it
from the grid. `draft.tags` (and after Apply, the applied `SearchParams.tags`) still contains it.
`getAnimeSearchVariables`/`getMangaSearchVariables` (search-atoms.ts:41-93) still send that tag in
the query, and pass `isAdult: false` — the combination is contradictory (querying an adult-only tag
with isAdult explicitly false) and will typically yield an empty/near-empty result set with zero
indication why. The only recovery is the sheet's "Reset" button, which wipes all filters, not just
the stuck tag. `getActiveFiltersCount` also keeps counting `tags.length > 0`, so the
`FilterButton`/"Apply (N)" badge stays inflated even though the tag grid shows nothing selected —
a second, compounding visual mismatch.

### 4. Selecting the "Upcoming" status filter together with "Release date" sort silently no-ops the status filter, while the UI still shows it as applied (medium, ux/correctness)
File: src/lib/search/search-atoms.ts:56-63 (identical pattern at :82-89 for manga)
```ts
status:
    params.sorting === "START_DATE_DESC"
        ? params.status.filter(s => s !== "NOT_YET_RELEASED").length > 0
            ? params.status.filter(s => s !== "NOT_YET_RELEASED")
            : undefined
        : params.status.length > 0
            ? params.status
            : undefined,
```
Scenario: user opens the filter sheet, selects only "Upcoming" under Status, and sets Sort by to
"Release date" (`START_DATE_DESC`), then Apply. `params.status` is `["NOT_YET_RELEASED"]`;
after the `START_DATE_DESC` branch strips it, the filtered array is empty, so `status` becomes
`undefined` — no status filter is sent to the server at all, and results include every status, not
just upcoming titles. Nothing in the UI reflects this: `SEARCH_STATUS` (search-constants.ts:482-488)
always offers "Upcoming" regardless of the chosen sort, the "Upcoming" chip in `MultiToggle` still
renders selected after Apply (draft is re-synced from `params` which still has
`status: ["NOT_YET_RELEASED"]` — only the *variables sent to the query* silently drop it, the stored
`SearchParams.status` array itself is untouched), and `getActiveFiltersCount` still counts the
status filter as active (badge shows "Filter (1)" or more). So the user sees an applied, highlighted
"Upcoming" filter with a non-zero filter count, but the actual result set is unfiltered by status —
a silent, unindicated conflict between two otherwise-independent filter axes.

## Rejected / near-miss (not reported)

- discover-hero-carousel.tsx auto-rotate `useEffect` recreates its `setInterval` every time
  `currentIndex` changes (dep array includes `currentIndex`). This is necessary (closure needs the
  latest index) and the 10s cadence is preserved correctly since each recreation happens right after
  the previous rotation — not a bug, just an unavoidable re-subscribe, no drift/leak (cleanup fires).
- `getCurrentSeason()`/`getPreviousSeason()` use device-local `new Date()` — flagged as a possible
  timezone bug per the brief but is NOT one: they only derive a season/year label (day-granularity),
  not a raw cross-boundary timestamp, so device timezone doesn't produce incorrect season selection
  in any user-observable way (season boundaries are month-based, not exact-instant).
- `useDiscoverRecentReleases`'s use of `new Date()`/`subDays` for airingAt bounds is NOT a timezone
  bug either — `airingAt_lesser/greater` are absolute Unix timestamps, unaffected by device TZ. (Its
  actual bug is the lack of memoization — see Finding 1 — not timezone correctness.)
- `isInteracting.current` (hero carousel) is only cleared in `onMomentumScrollEnd` and the
  `!isActive` effect; if a drag begins but momentum scroll end somehow never fires, auto-rotate
  would stay paused indefinitely. Very hard to hit in practice on iOS ScrollView (momentum end is
  reliably delivered even for a tap-only "drag"), no concrete repro found — capped as a non-finding.
- `FormField label={isAnime ? "Format" : "Format"}` (search-filter-sheet.tsx:127-130) — dead
  ternary, both branches identical. Cosmetic only, not worth a finding on its own.
- No debounce logic exists anywhere in `src/lib/search/` — the brief calls out "search debouncing"
  as a concern area, but the debounce (if any) must live in the screen/route component that owns the
  search text input, which is outside this scope (app/ router files). Nothing to audit here; noting
  as a coverage gap rather than a finding.
- No pagination or row-virtualization code exists in scope either — `discover-queries.ts` hooks are
  all fixed `page: 1`/`perPage: N` with no cursor/infinite-query logic, and the only list-rendering
  component in scope (hero carousel) uses a plain `ScrollView` over a hard-capped `MAX_ITEMS = 12`
  array, not a virtualized list. Actual list/grid virtualization for search results and library rows
  is presumably in app/ screens, out of scope.
- Reviewed `SEARCH_YEARS` construction (search-constants.ts:506-509) for off-by-one: produces
  `CURRENT_YEAR+1` down to `1990`, 38 entries when CURRENT_YEAR=2026 — correct, includes next year
  for "upcoming" and stops at 1990 by design, not a bug.
- Checked `getActiveFiltersCount`/`countActiveCollectionFilters` for parity with the actual filters
  each sheet renders — genre/tags/status/format/season/year/minScore/country/isAdult all match one
  field each, no double count or missing count found beyond the two conflict scenarios above.

## Coverage

Read every file in scope in full (6 files, ~1740 lines). Cross-checked the two dead constant
exports, the query-key construction for the recent-airing hook, and the default-sorting settings
plumbing in adjacent (out-of-scope) files only to establish concrete reachability/evidence for
findings 1 and 2 — did not audit those adjacent files for their own bugs. Did not cover: the actual
Discover/Search/My-Lists *screens* that consume these hooks/sheets (app/ router files — own scope
for another agent), the Missing/Upcoming section component itself (lives in src/api/hooks +
presumably a component outside the three assigned directories), or list virtualization/pagination
(no such code exists inside the assigned scope to review).
