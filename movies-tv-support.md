# Movies / TV support — design writeup

Status: investigation / proposal. No code written yet.

## Goal

Add first-class movies & TV support to Seanime, surfaced in Discover (tabs
alongside Anime / Schedule / Manga) and Search (type dropdown), backed by a
fork of the self-hosted **aiometadata** (TMDB) addon.

## Hard constraints (binding)

1. **Do not share AniList IDs.** Movies/TV must use their own ID namespace.
   Reusing AniList-shaped IDs risks the tracking layer trying to sync a movie
   to a real AniList entry → corrupted tracking on both sides.
2. **Keep movies/TV code separate from the anime portions.** It must not leak
   into the anime Discover rows, anime Search results, anime library
   collection, or AniList tracking. Anime stays exactly as it is today.

These two constraints rule out the obvious shortcut (see below).

## Why not Custom Sources (the rejected shortcut)

Seanime already has a pluggable non-AniList path: **Custom Sources**
(`internal/customsource/`, `internal/extension/hibike/customsource/types.go`).
A JS extension returns `anilist.BaseAnime`-shaped media; IDs get packed into a
reserved high-bit space (`2^31+`) so they don't *literally* collide with
AniList IDs.

It's rejected here because it deliberately does the two things we must avoid:

- It **reuses the `BaseAnime` type and flows into the anime collection**, so
  the content shows up in anime Search / library surfaces → pollution.
- It rides the anime tracking path. Even with local-only tracking, it blurs the
  separation we want.

Custom Sources are the right tool for "extra anime not on AniList." They are the
wrong tool for "a separate movies/TV vertical that must stay walled off."

## Architecture reality — the seam

The codebase splits cleanly into a content-agnostic playback core and an
anime-coupled layer on top.

**Content-agnostic — reuse as-is (no AniList in the signature):**

| Layer | Why reusable |
|---|---|
| `internal/nativeplayer`, `internal/videocore` | Takes `PlaybackInfo` (URL + tracks), not a media ID |
| `internal/mkvparser`, `internal/matroska`, `internal/pgs` | Pure container/subtitle parsing |
| `internal/mediastream` (transcode) | Operates on a file/URL |
| `torrentstream.Client.addTorrentMagnet(magnet)` | Raw magnet → torrent |
| `debrid` providers `GetTorrentStreamUrl(opts, …)` | Takes hash/magnet, returns URL |
| `internal/directstream` | Pre-resolved direct URLs (already infohash-less) |

**Anime-coupled — needs a parallel, not a reuse:**

| Layer | Coupling |
|---|---|
| `torrentstream.Repository` / `finder.go` | Signature is `*anilist.CompleteAnime` + AniDB episode mapping |
| `debrid/client.Repository` / `stream.go` / `finder.go` | `*anilist.BaseAnime` + `MediaId int` |
| `internal/library` (scan/collection) | AniList-keyed entries |
| `internal/platforms/*` (AniList platform, tracking) | AniList API |
| `internal/api/metadata` (AniList/anizip/animap) | Anime metadata |

So: **the player below the finder is free; the finder/repository wrappers that
pick a torrent for "media X episode N" are where the anime assumptions live, and
where the movies/TV vertical gets its own copy.**

## Proposed design: a separate "Media" vertical

A parallel subsystem (working name **`media`** — movies + TV) that mirrors the
anime path's *shape* but never touches AniList types, anime collection, or
AniList tracking.

### ID & tracking strategy

- Own namespace, e.g. string IDs `movie:<tmdb_id>` / `tv:<tmdb_id>:s<season>`.
  No integer AniList-shaped IDs anywhere in this vertical.
- Own progress/tracking store (new tiny table, e.g. `media_tracking`), local
  only. **Never** calls the AniList platform. Movie = watched/unwatched + resume
  position; TV = per-episode progress keyed by `tmdb_id + season + episode`.
- Continuity/resume (`internal/continuity`) — check whether its watch-history
  store is content-agnostic enough to reuse with a namespaced key, or whether it
  needs a sibling store. (Open decision.)

### Backend

- New package `internal/media/` (NOT `customsource`):
  - `provider.go` — talks to the aiometadata fork (search, details, trending,
    episode/season lists). Its DTOs are TMDB-shaped, **not** `BaseAnime`.
  - `tracking.go` — local progress store.
  - `stream.go` — wraps the content-agnostic torrent/debrid/directstream core
    with a `MediaPlaybackRequest { id, season?, episode?, torrentOrUrl }` input.
    Reuses `addTorrentMagnet` / `GetTorrentStreamUrl` / directstream directly;
    does **not** import `torrentstream.Repository`.
- New handler group `internal/handlers/media_*.go`, routes under
  `/api/v1/media/...` (search, discover/trending, details, episodes, play,
  progress). Separate from `/api/v1/custom-source` and the anime routes.
- Torrent search for movies/TV reuses the existing torrent provider extensions
  (`hibiketorrent`) — those search by query string, not by AniList ID, so they
  work for "Dune 2024 2160p" the same as for anime.

### aiometadata fork — endpoints needed

Fork adds Seanime-shaped JSON endpoints (TMDB-backed):

| Endpoint | Returns |
|---|---|
| `GET /search?q=&type=movie\|tv&page=` | list: id, title, year, poster, backdrop, type, overview |
| `GET /details/{type}/{id}` | full detail: genres, runtime, status, rating, cast |
| `GET /tv/{id}/seasons` | seasons → episodes (number, title, air date, still) |
| `GET /trending/{type}?page=` | trending list (powers Discover tabs) |
| `GET /popular/{type}?window=` | popular/top (extra Discover rows) |

Response shape lives in the writeup once Phase 1 starts; key point — it is a new
schema, not AniList's.

### Frontend (separate surfaces, no pollution)

- **Discover:** add `tv` / `movies` to the existing `StaticTabs` in
  `discover/page.tsx` and the `__discord_pageTypeAtom` union. Each tab renders
  its **own** containers (`discover-trending-movies-real.tsx`, etc.) hitting
  `/api/v1/media/...`. The existing anime/manga/schedule tabs are untouched —
  no anime query learns about movies/TV.
- **Search:** add `tv` / `movie` to the search `type`
  (`advanced-search-options.tsx`, currently `"anime" | "manga"`). When type is
  tv/movie, the list and options components branch to the media API instead of
  AniList. Anime/manga search paths unchanged.
- **Detail + player:** new route `/media/[type]/[id]` with its own entry page
  (episode/season list for TV, single play for movies), reusing the **native
  player UI component** (content-agnostic) but its own data wiring.
- Web type defs: new `MEDIA_*` TS types in a separate file; do not extend `AL_*`.

## Phased plan

1. **aiometadata fork** — stand up the 5 endpoints above against TMDB. Verifiable
   independently of Seanime.
2. **Backend `internal/media`** — provider client + handlers + routes. Wire the
   stream layer to the existing torrent/debrid/directstream core. Local tracking
   store.
3. **Discover tabs** — new `tv`/`movies` tabs + containers, media API queries.
4. **Search** — type dropdown options + branched list/options.
5. **Detail + playback** — `/media/[type]/[id]` page reusing the native player.
6. **Polish** — resume/continuity decision, watchlist, settings toggle to
   enable/disable the whole vertical (mirror `enableManga`).

## Open decisions

- **Continuity reuse vs. sibling store** for resume positions (needs a look at
  `internal/continuity` key assumptions).
- **TV granularity**: track per-episode (recommended) vs per-season.
- **Library/local files**: does movies/TV need local-file scanning, or is it
  streaming-only (torrent/debrid/directstream) for v1? Streaming-only is the
  lazy v1; local scan is a separate large effort and can wait.
- **Settings gate**: one toggle `library.enableMedia` so users who only want
  anime see zero movies/TV UI.

## Risks / notes

- The torrent/debrid **finder** logic (AniDB episode mapping, batch handling) is
  anime-tuned. The media vertical needs its own, simpler episode→file matching
  for TV (season/episode parsing). Movies are trivial (single file).
- Keep the settings gate default-off so the anime-only experience is byte-for-byte
  unchanged when the feature isn't enabled.
- No anime code path should ever import from `internal/media`, and vice-versa,
  except the shared content-agnostic player/stream primitives.
