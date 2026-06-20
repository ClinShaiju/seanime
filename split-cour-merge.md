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

## Plan

### Backend (data foundation — low risk)
- `internal/library/anime/merged_season.go`: given the franchise + a target season
  number, gather its cours (AniList entries), build each cour's `Entry` (reuse
  `NewEntry`), and concatenate their `Episodes` into one list:
  - **Display number** renumbered continuously (1..36).
  - Each episode keeps its **source `BaseAnime` + source `progressNumber`** (so
    playback/progress can route correctly).
  - Carry a per-episode `sourceMediaId` so the frontend doesn't have to infer it.
- New handler `HandleGetMergedSeason(rootId, seasonNumber)` → a merged `Entry`-shaped
  payload. Read-only; doesn't touch the existing single-entry path.

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
