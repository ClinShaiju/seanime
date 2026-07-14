# Parity audit — Discover, Schedule, Search (tenji vs web/Denshi)

Scope: web discover page rows (incl. Aired Recently), schedule page (Missing/Upcoming),
advanced search params (tags, min score, anything newer). Baseline: Tenji v0.1.24
(2026-07-09, HEAD 65f8baf). Web audited at current HEAD (2026-07-10).

## Method

- `git log --oneline 20596e8b..HEAD -- seanime-web/src/app/\(main\)/discover seanime-web/src/app/\(main\)/schedule seanime-web/src/app/\(main\)/search`
  → **zero commits**. Discover/schedule/search web code is unchanged since the v3.9.0
  merge (2026-07-04, the commit tenji's parity batch was audited against). So there are
  no web deltas to reconcile for this area — verifying against current HEAD is
  equivalent to verifying against baseline-audit-time code.
  (Initial attempt with `--since=2026-07-04` returned nothing too, but that's a red
  herring: those paths' historical commits carry pre-merge upstream author/commit dates
  even though the merge landed 2026-07-04 — confirmed by re-running without `--since`
  and cross-checking with the commit-range form.)

## Discover

Web: `seanime-web/src/app/(main)/discover/page.tsx` (189 lines) orchestrates, per
Anime/Schedule/Manga `StaticTabs` mode:
- Anime: Trending Right Now (`discover-trending.tsx`, w/ genre selector + rotating hero
  banner state), **RecentReleases** ("Aired Recently", `schedule/_containers/recent-releases.tsx`),
  Top of the Season, Best of Last Season, DiscoverMissedSequelsSection ("You Might Have
  Missed", `discover-missed-sequels.tsx`), Coming Soon, Trending Movies.
- Schedule tab: `DiscoverAiringSchedule`.
- Manga tab: Trending Manga/Manhwa/Manhua by country (JP/KR/CN).

Aired-Recently query (web, `recent-releases.tsx`):
```
useAnilistListRecentAiringAnime({ page:1, perPage:50,
  airingAt_lesser: now, airingAt_greater: now-14d })
```
filtered `!isAdult && type===ANIME && countryOfOrigin===JP && format!==TV_SHORT`.

Tenji: `src/components/features/discover/discover-queries.ts` (131 lines) —
`useDiscoverTrendingAnime` (genre param matches web's filterable trending row),
`useDiscoverCurrentSeasonAnime`, `useDiscoverPastSeasonAnime`, `useDiscoverUpcomingAnime`,
`useDiscoverTrendingMovies`, `useDiscoverMissedSequels` (wraps
`useAnilistListMissedSequels`, same as web), `useDiscoverRecentReleases` — doc comment
*"mirroring the web app's 'Aired Recently' row"*, identical 14-day window/params.
`useDiscoverTrendingManga(country)` for JP/KR/CN.

`app/(app)/(tabs)/discover/index.tsx` (691 lines): full anime/manga toggle, hero
carousel (native reimplementation of web's rotating-banner hero,
`DiscoverHeroCarouselBackdrop`/`...InteractionLayer`/`useDiscoverHeroCarouselController`),
lazy section activation, sections `trending` (w/ `MediaGenreSelector` using
`SEARCH_MEDIA_GENRES`), `current-season`, `past-season`, `upcoming`, `movies`, `missed`,
`recent-releases`; manga `jp`/`kr`/`cn`. Section titles match web exactly ("Trending
Right Now", "Top of {season}", "Best of {season}", "Coming Soon", "Trending Movies",
"You Might Have Missed", "Aired Recently", "Trending Manga/Manhwa/Manhua").

**Verdict: fully ported**, including the specific 14-day Aired-Recently window and the
missed-sequels row, both re-verified against current web HEAD (unchanged since 2026-07-04).

## Schedule

Web: `schedule/page.tsx` (41 lines) orchestrates:
- `missing-episodes.tsx` (157 lines): "Missing from your library" carousel **plus an
  Accordion "Silenced episodes" section** (icon `LuBellOff`), driven by
  `useHandleMissingEpisodes(data)` splitting `{missingEpisodes, silencedEpisodes}`.
  Silence is toggled per-entry via `entry/_containers/entry-actions/anime-entry-silence-toggle.tsx`
  and persisted via `schedule/_lib/handle-missing-episodes.ts` +
  `_atoms/missing-episodes.atoms.ts` + `_hooks/missing-episodes-loader.ts`.
- `schedule-calendar.tsx` (786 lines): month-grid (desktop) / day-list (mobile) calendar,
  settings popover (week-starts-on Mon/Sun, status filter Watching/Planning/Completed/Paused,
  "indicate watched episodes" switch, "disable image transitions" switch), per-day modal,
  season-finale flag, "+N more" popover.
- `upcoming-episodes.tsx` (85 lines): "Upcoming episodes" carousel via
  `useGetUpcomingEpisodes()`.

Confirmed via `grep -rli "silenc"` (real, non-generated hits only):
`entry/_components/meta-section.tsx`, `entry/_containers/entry-actions/anime-entry-silence-toggle.tsx`,
`schedule/_components/missing-episodes.tsx`, `schedule/_lib/handle-missing-episodes.ts`,
`_atoms/missing-episodes.atoms.ts`, `_hooks/missing-episodes-loader.ts`.

Tenji: `app/(app)/(tabs)/schedule/index.tsx` (722 lines) — `MissingEpisodesRow` ("Missing
from your library"), week-based `WeekDaySelector` (native-appropriate substitute for
web's month grid) w/ per-day counts, `ScheduleGrid` (3-col grid, time/episode/finale
overlays), `UpcomingEpisodesRow` ("Upcoming episodes"), `ScheduleSettingsSheet` (status
filter Watching/Planning/Completed/Paused/**Repeating** [extra vs web] + "Indicate
watched episodes" toggle), `MonthYearPicker` (jump-to-month). Code comments explicitly
claim parity: *"mirrors the web app's useHandleMissingEpisodes: hide adult entries unless
opted in"*, *"mirrors the web app's useMissingEpisodeSpoilers"*. `isSeasonFinale` → "FIN."
label, matches web.

**No "silence" functionality anywhere in tenji.** Re-confirmed this pass with two more
targeted greps for likely alternate names (`mute`, `ignoreEpisode`, `hideFromSchedule`,
`excludeFromMissing`) across `src/` and `app/` — only false-positive hits (`Surface
variant="muted"`, `useMutation`, etc.), no real silence/mute-schedule feature. This is a
genuine, newly-identified gap for this audit pass (not in the given deferred-known list).

**Verdict**: Missing Episodes + Upcoming ported faithfully (native week-view UX carries
the same substantive behavior: status filtering, watched-indication, adult filtering,
spoiler handling). Silenced-episodes accordion + per-entry silence toggle = **missing**.

## Search (advanced search)

Web filters (`search/_components/advanced-search-options.tsx`, 265 lines) + params
(`search/_lib/advanced-search.atoms.ts`, `__advancedSearch_paramsAtom`): Title (debounced
text), Type (anime/manga), Sorting (Trending/Release date/Highest score/Most
popular/Episodes|Chapters), Genre multi-Combobox, Tags multi-Combobox (adult-filtered
against `ADVANCED_SEARCH_MEDIA_TAGS`, full AniList tag taxonomy w/ id/name/description/
category/isAdult), Format (anime: TV/MOVIE/ONA/OVA/TV_SHORT/SPECIAL; manga: MANGA/ONE_SHOT),
Country of origin (manga: JP/KR/CN/TW), Season (anime), Year (~70yrs), Status
(**array** — `AL_MediaStatus[]`, confirmed multi-select via atom type, not single-select
as first assumed from the options component alone), Min Score (**single-value** select,
options 9→1), Adult switch (conditional on server `enableAdultContent` setting),
highlighted clear-filters button. Results: `advanced-search-list.tsx` groups by franchise
(`useGroupedById`) and paginates via click-to-"Load more" (`fetchNextPage`).

Tenji: `src/lib/search/search-constants.ts` (510 lines) — `SEARCH_MEDIA_GENRES` (18,
exact match to web's `ADVANCED_SEARCH_MEDIA_GENRES`), `SEARCH_MEDIA_TAGS` (full
name+isAdult AniList taxonomy, ~420 entries, same taxonomy as web's
`ADVANCED_SEARCH_MEDIA_TAGS`), `SEARCH_MIN_SCORES` (9→1, exact match), `SEARCH_SEASONS`,
`SEARCH_FORMATS_ANIME`/`SEARCH_FORMATS_MANGA`, `SEARCH_COUNTRIES_MANGA` (JP/KR/CN/TW),
`SEARCH_STATUS` (5 values, matches), `SEARCH_SORTING_ANIME`/`SEARCH_SORTING_MANGA`
(Episodes/Chapters variant preserved), `SEARCH_YEARS`.

`search-atoms.ts`: `SearchParams` type is a 1:1 field match to web's
`__advancedSearch_paramsAtom` shape (title, sorting, genre, tags, status[], format,
season, year, minScore, isAdult, countryOfOrigin, type). `getAnimeSearchVariables`/
`getMangaSearchVariables` mirror web's query-variable derivation, including the
`SEARCH_MATCH` sort-prepend when a title is present and the same
`START_DATE_DESC`-drops-`NOT_YET_RELEASED`-from-status quirk.

`search-filter-sheet.tsx` (281 lines): Sort by, Format, Country of origin (manga only),
Season (anime only), Year (horizontal chip scroll vs web's select — same option set),
Status (`MultiToggle`), Genres (`MultiToggle`), Tags (`MultiToggle`, adult-filtered same
as web), Minimum Score (`InlineSelect`, single-value 9→1 — exact UI-type match to web),
Adult Content switch (same conditional gating on server setting).

`discover/search.tsx` (225 lines): debounced title search (350ms, matches web's debounce
pattern), type toggle resets filters but preserves title (same as web's behavior),
infinite-scroll via `onEndReached` (functionally equivalent to — arguably smoother than —
web's manual "Load more" click), franchise-season grouping via `useGroupedById` on
results (same as web `advanced-search-list.tsx`), active-filter-count badge on the
Filter button.

**Verdict: fully ported**, including Tags and Min Score (explicitly called out in task
scope) and Status as a genuine multi-select on both sides (not a gap — my working notes
initially suspected web Status was single-select from the options component's `<select>`
styling alone; the atom type `AL_MediaStatus[]` on web confirms it is multi-select,
matching tenji's `MultiToggle`, so no discrepancy to report).

## Not chased further (out of core scope for this area)

- Web `search/page.tsx` renders a "Custom sources" quick-link button (linked to
  `/custom-sources`, powered by `useListCustomSourceExtensions`) next to the search
  title. This is entry into the plugin/extension system, not a search filter — treated
  as out of scope for discover/schedule/search parity (extensions are a separate large
  area, not enumerated in my task scope or the given ported/deferred lists).
- Web schedule-calendar's "disable image transitions" switch is a web-only CSS
  perf/visual toggle with no meaningful native analogue; not treated as a gap.
- Web calendar's week-starts-on Mon/Sun setting and tenji's `Repeating` status filter
  option (which web's calendar settings doesn't have) are minor UX deltas in the
  calendar-view substitution, not scored as separate gaps given the two calendars are
  intentionally different native-appropriate UX (week-view vs month-grid), already
  covered qualitatively above.
