# Split-cour merge — plan

Status: starting. Builds on the season-grouping work (season-select-support.md).

## Goal

Combine split cours into one season with a **continuous episode list**. E.g.
Ascendance of a Bookworm: AniList has S1 (14ep), S2 (12ep), S3 (10ep) as separate
entries, but TMDB lumps them as **Season 1 (36ep)**. The entry page should show a
single **"Season 1"** with episodes 1–36, not "Season 1 Part 1/2/3". Re:Zero is the
same shape (S1 = 2 cours, S2 = 2 cours).

## Why this is more than UI (confirmed by investigation)

The entry page episode list, **playback, progress, watch-history, and autoselect**
are all keyed off the page's single `entry.mediaId` (`episode-section.tsx` uses
`entry.mediaId` everywhere). The `Episode` struct *does* carry its own
`BaseAnime` (`internal/library/anime/episode.go`), but the section ignores it.

So a merged "Season 1" view must become **per-episode-media-aware**: episode 20 of
the merged season is really episode 6 of the S2 entry, and playing / marking it
watched must route to the S2 AniList id + S2-relative episode number. AniList
tracking stays per-entry (each cour keeps its own progress) — the merge is a
presentation layer that fans actions back to the right cour.

## Grouping the cours

Cours of one season = franchise members sharing the same TMDB `seasonNumber`
(already resolved per id by the FranchiseResolver). Order by air date. The season
switcher shows one chip per distinct season number; a multi-cour season expands to
the merged episode list. (Caveat: an unreleased cour with no animap mapping gets a
wrong season number — e.g. Ascendance "Adopted Daughter" — until it airs; that's an
upstream gap, not this feature.)

## Three things to route per episode (key design)

Each merged episode must carry, so the frontend/back can fan actions to the right place:
- **source cour `mediaId`** (`episode.baseAnime.id`) → AniList progress (per-cour).
- **cour-relative `episodeNumber`/`progressNumber`** → AniList progress value.
- **`absoluteEpisodeNumber`** → batch/torrent matching. A 2nd-cour ep 1 is ep 13 in a
  batch; Seanime currently passes the cour-relative 1, so autoselect must use the
  absolute number for merged seasons. (`Episode.AbsoluteEpisodeNumber` already exists.)

AniList stays per-cour: UI "15/24" = cour1 12/12 (completed) + cour2 3/12 (watching).

## Plan

### Backend (DONE — deployed)
- `HandleGetMergedSeason(rootId, seasonNumber)` → `anime.MergedSeason`
  (`internal/handlers/anime_franchise.go`, route
  `/api/v1/library/anime-entry/{id}/merged-season/{season}`). Reuses the cached
  franchise group, filters Seasons to the target season number (= the cours), and
  for each cour builds its **episode collection** (`NewEpisodeCollection` — the
  view-agnostic metadata source that works for Debrid/Torrent streaming, unlike
  `NewEntry` which is local-file only) and concatenates the main episodes. Each
  `Episode` retains its source `BaseAnime` + cour-relative + absolute numbers.
  `MergedCour` carries per-cour progress + the continuous start episode.
  Franchise-build logic extracted to `resolveFranchiseGroup` (shared).

### Frontend (NEXT — the careful part, needs live testing)
- Season switcher: collapse same-season cours into one "Season N" chip; selecting a
  multi-cour season loads the merged payload instead of navigating to a cour.
- Render the merged episode list with continuous display numbers (array position).
- **Per-episode routing**: play / progress / watch-history use the episode's source
  `baseAnime.id` + cour-relative number — NOT the page `entry.mediaId`. Must work in
  the Debrid view (user's setup) — validate live; can't be tested from the build host.
- **Autoselect/torrent**: when searching a batch for a merged-season episode, match on
  the **absolute** number.

### Frontend (the careful part — playback rework)
- Season switcher: collapse same-season cours into one "Season N" chip; selecting a
  multi-cour season loads the merged payload.
- `episode-section.tsx`: when rendering a merged season, route **per-episode** —
  play / progress / watch-history use the episode's `sourceMediaId` +
  `progressNumber` instead of `entry.mediaId`. This is the load-bearing change;
  validate playback + progress sync carefully (can't be tested from the build host —
  needs live testing).
- Autoselect / torrent search likewise key off the episode's source cour.

### Staging
1. Backend merged-season builder + endpoint (buildable/testable).
2. Frontend: render merged episode list (no playback yet) — visual check.
3. Wire per-episode playback + progress; test live.
4. Autoselect + download routing.
5. Gate behind the existing `groupSeasons` toggle.

## Open decision

When you mark merged-episode 20 watched, it sets the S2 cour's AniList progress to
6. Continue-watching / next-episode then operates across cours. Confirm that's the
desired behavior (vs. keeping cours fully independent for tracking).
