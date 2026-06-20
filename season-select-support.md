# Season-select support (one entry, N seasons) — implementation plan

Status: **finalized plan**. No code written yet.

## Goal

Show a multi-season anime as **one** entry with a season switcher (Stremio-style)
instead of one entry per season, across the entry page first, then Library,
Search, and Discover. Two view modes:

- **Seasons** — group TMDB seasons only; OVAs/movies/specials go to a separate
  "watch order" section.
- **Watch order** — the whole franchise (TV + OVA + movie + special) sorted by
  **air date**, with a "next watch" = next unwatched item.

Decisions locked: watch order = **air date**; season grouping = **TMDB id +
per-episode season number**; tracking stays **per-entry, presentation-only**.

## Why this works (and its one hard edge)

Seanime runs on AniList, which models every season as an independent entry (own
id/progress/score/status). Library is a 1:1 mirror of the AniList
`MediaListCollection`; Search/Discover are AniList passthroughs. So grouping is a
**presentation overlay** — entries keep their real identity underneath and every
action still routes to the correct AniList id. No new tracking store.

The grouping data **already loads**:

- `animap` (`internal/api/animap/animap.go`, queried by `anilist_id`) →
  `AnimeMapping.TheMovieDbID` + per-episode `SeasonNumber`/`Type` (TV/OVA/Movie).
- `metadata_provider` already surfaces `AnimeMetadata.Mappings.ThemoviedbId`
  (`internal/api/metadata/types.go:42`) per entry, cached 1h, and ships it to the
  frontend with the entry's metadata.

So: **group AniList entries by shared non-empty `ThemoviedbId`; order seasons by
the per-episode `SeasonNumber`.** That gives the canonical season number AniList
lacks.

The one hard edge — **anime movies are separate TMDB ids** (TMDB *movies*, not
seasons), so they don't share the TV show's tmdb id. That's *correct* for
seasons-only grouping; the movie/OVA bucket comes from AniList relations
(`FetchMediaTree`) + air dates instead. Coverage gaps (entries with no
`ThemoviedbId`) fall back to the relation tree, else stay ungrouped/flat.

## Backend

### New: `internal/library/anime/franchise.go`

Core types + the resolver/grouper:

```
FranchiseRef struct { TmdbId string; SeasonNumber int }      // per anilist id

FranchiseGroup struct {
    RootMediaId int                 // lowest season / earliest start date
    RootMedia   *anilist.BaseAnime
    Seasons     []*GroupedEntry     // TV, sorted by SeasonNumber
    Extras      []*GroupedEntry     // OVA/Movie/Special (relation-sourced)
}
```

- `ResolveFranchiseRef(anilistId) FranchiseRef` — read from persistent cache;
  on miss, fetch via the existing metadata provider (which already calls animap),
  take the entry's `ThemoviedbId` and the `SeasonNumber` of its first mapped
  episode. Rate-limited like `FetchMediaTree`. **ponytail: entry season = first
  mapped episode's SeasonNumber; min-season fallback if split-cour matters later.**
- `GroupEntriesByFranchise(entries) []*FranchiseGroup` — bucket by non-empty
  `TmdbId`; entries without one fall back to a `FetchMediaTree` SEQUEL/PREQUEL
  chain; still nothing → singleton group. Root = lowest season number, tiebreak
  earliest start date.
- `WatchOrder(group) []*GroupedEntry` — Seasons ∪ Extras sorted by start/air
  date. `NextWatch(group, listData)` = first item not fully watched in that order.

**Extras source:** `FetchMediaTree` already gathers SEQUEL/PREQUEL relations
(movies/OVAs included). Reuse it to populate `Extras`; do **not** invent a new
relation walker.

### Persistent cache

`anilist_id -> FranchiseRef`. Reuse the existing file-backed cache infra
(`internal/api/anilist/localcache.go` pattern) rather than a DB migration —
**ponytail: file cache, switch to a table only if it needs cross-process
querying.** First library build after enabling fills the cache (slow once);
subsequent builds read from disk.

### Wiring

- `internal/database/models/models.go` `LibrarySettings` (line 69, beside
  `EnableManga`): add `GroupSeasons bool` (default false) and
  `SeasonViewMode string` (default `"seasons"`). Plumb through the settings
  handler + web settings types (mirror `enableManga` end to end).
- `LibraryCollection` (`internal/library/anime/collection.go`): when
  `GroupSeasons` is on, add `Franchises []*FranchiseGroup` hydrated by grouping
  **within each status list** (entries collapse only with same-status siblings —
  the franchise detail page is where you see all seasons regardless of status).
  **ponytail: within-status grouping avoids a thorny cross-status merge and
  matches AniList reality (each season has its own status).** Existing flat
  `Lists` stays untouched so default-off is byte-for-byte unchanged.
- New thin handler `HandleGetFranchise(anilistId)` → `FranchiseGroup` for the
  entry page's switcher/watch-order, under `/api/v1/anime/franchise/...`.

## Frontend

- **Settings:** `GroupSeasons` toggle + `SeasonViewMode` select (library settings
  container; mirror the manga toggle).
- **Entry page (Phase 1):** season-switcher dropdown (sibling seasons from
  `HandleGetFranchise`, ordered by season number) that navigates to the selected
  season's existing entry page — no data merge, just navigation. Plus a
  watch-order list + "next watch" button when `SeasonViewMode = watchOrder`.
- **Library / Search / Discover:** when grouping on, collapse cards sharing a
  franchise root into one card with an "N seasons" badge; expand to the season
  list. Search/Discover are flat passthrough lists — collapse via the same
  franchise lookup. (`advanced-search-list.tsx`, anime-library feature,
  discover containers.)
- **Types:** small `Franchise` TS type; do not overload existing AniList types.

## Phases (each ships independently, default-off gate throughout)

0. **Franchise resolver** — `franchise.go` + persistent cache + grouping/watch-
   order/next-watch. Backend only, no UI. **Check:** unit test — feed Danmachi's
   season ids + the movie, assert 5 seasons grouped by tmdb id and ordered by
   season number, movie in `Extras`, watch order ascending by date.
1. **Entry-page season switcher + next-watch** — highest value, lowest risk;
   lists stay flat. Adds the settings toggle + `HandleGetFranchise`.
2. **Library collapse** — group within status lists, behind `GroupSeasons`.
3. **Search + Discover collapse** — apply grouping to passthrough lists.
4. **Polish** — cache warm/refresh, coverage-gap fallback copy, settings UI text.

## Locked decisions

- Watch order = **air date** (no curated insertion points — that data doesn't
  exist machine-readable).
- Season grouping = **TMDB id + per-episode `SeasonNumber`**; fallback
  `FetchMediaTree`; else flat.
- Tracking = **per-entry, presentation-only**; no new store.
- Library grouping = **within-status only**; full season span lives on the
  franchise detail/entry page.
- Setting = `library.groupSeasons` (default off) + `library.seasonViewMode`
  (`"seasons"` default | `"watchOrder"`).
