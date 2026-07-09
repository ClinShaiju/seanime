# Movies / TV vertical + Trakt tracking — implementation plan

Status: **design complete, ready to implement**. Written 2026-07-06.
Supersedes `movies-tv-support.md` (the earlier sketch); its two hard constraints carry over unchanged.
Audience: the implementing model (Opus). Everything below is instruction, not discussion — where a
decision was open, it has been made and is stated as such.

Research inputs: full backend + frontend seam surveys of this repo (file:line anchors below are from
2026-07-06 `main`, may drift a few lines), the working Seanime↔Stremio prototype in
`H:/Projects/AIOStreams/packages/seanime-extensions/`, [AIOMetadata](https://github.com/cedya77/aiometadata),
[Stremio-Kai](https://github.com/allecsc/Stremio-Kai), and the [Trakt API docs](https://trakt.docs.apiary.io/)
([auth](https://docs.trakt.tv/docs/authentication-oauth)).

---

## 0. TL;DR — the decisions

| Question | Decision |
|---|---|
| Where does movies/TV live? | New walled-off vertical: `internal/media` (Go) + `/api/v1/media/*` + `seanime-web/src/app/(main)/media/` — **not** Custom Sources, **not** the anime collection |
| ID space | **Strings**, Stremio-style: `movie/tt0111161`, `tv/tmdb:1396`. Never integers. Zero overlap with AniList by construction |
| Metadata source | **Native TMDB client** in `internal/media` (the same pattern as anime's native AniList/anizip clients — no addon, no proxy). Embedded default API key so it works out of the box; optional user-key override in settings to avoid rate limits. AIOMetadata was **reference material only** (which sources/mappings a mature implementation uses), not a dependency |
| Stream source | **AIOStreams Search API** (`GET {base}/api/v1/search?type=&id=`) — our fork already returns `streamUrl` for pre-resolved debrid. Server-side Go client, not a goja extension. Manifest URL auto-resolved from the already-installed AIOStreams extension's config (settings override available) — no re-entry of anything |
| Playback | New content-agnostic entry point in `internal/directstream` that skips the `anime.EpisodeCollection` machinery; reuses the whole stream/serve/videocore pipeline below it |
| Tracking (movies/TV) | Local per-user DB table + separate continuity buckets. Plus Trakt scrobble |
| Trakt | New `internal/trakt` package. Device-code OAuth (user's own API app credentials), per-user tokens, **stop-only scrobbling v1**. Covers movies, TV, **and anime** (mapping via anizip TVDB season/episode data — already available) |
| Anime | **Byte-for-byte untouched** when `enableMediaVertical` is off. Trakt-for-anime is a separate additive hook + its own toggle |
| Separation in UI | "Movies" and "TV" are separate discover tabs / search types / nav links, backed by the one `internal/media` vertical with a `type` discriminator |

---

## 1. Hard constraints (binding — carry over from the original writeup)

1. **No AniList ID sharing.** Movies/TV use string keys (`movie/tt…`, `tv/tmdb:…`). Nothing in the
   vertical may produce or consume an AniList-shaped `int` media id.
2. **Anime stays exactly as it is.** No anime code path imports `internal/media`; no movies/TV data
   appears in anime Discover rows, anime Search, the anime library collection, or AniList tracking.
   With the feature flag off, the app is behaviorally identical to today.
3. New (this iteration): **Trakt must work for all three content types** — movies/TV natively, anime
   via ID mapping — and must be per-user (the fork is multi-user).
4. New: movies/TV must **tightly integrate** — continue watching on Home, resume positions,
   discover/search parity — while remaining a separate vertical internally.
5. New (user clarification 2026-07-06): **first-class native UX, zero extension plumbing.** The end
   state is one streaming app with three peer verticals — Anime, Movies, TV — each with its own nav
   entry, discover tab, search type, detail pages, and continue-watching presence. No Custom
   Sources, no "install extension from URL" flows, no hidden menus. The prototype extensions in the
   AIOStreams fork (§2.1) are **research prior art only** — none of them ship as part of this
   feature, and the movies/TV experience must never depend on one being installed.
6. New (user clarification 2026-07-06): **zero required configuration.** Metadata is fetched
   natively by the Go server (same way anime already talks to AniList/anizip directly), with an
   embedded default TMDB key so browsing works the moment the flag is on. Everything else the
   feature needs already exists in the app: debrid API keys (TorBox etc.) are configured, AIOStreams
   is installed — reuse them, never ask twice. The only *new* settings are **optional**: a personal
   TMDB API key (rate-limit headroom, the same idea as AIOMetadata's bring-your-own-keys) and the
   Trakt connection (inherently opt-in).

---

## 2. Research findings

### 2.1 Prior art: the prototype in our AIOStreams fork (study before writing code)

`H:/Projects/AIOStreams/packages/seanime-extensions/src/` contains a **working** bridge that surfaced
Stremio catalogs into Seanime via Custom Sources. It is the strongest evidence for why the vertical
must be first-class, and several pieces port over directly:

- `lib/stremio-id.ts` — encodes Stremio string ids (`tt…`, `tmdb:…`, `kitsu:…`) into Seanime's 40-bit
  custom-source int space. **This entire file exists only because Custom Sources force int ids.**
  The new vertical uses string keys, so this problem evaporates. Do not port any of it — the only
  id assembly the vertical does is building the `tt123:2:5` composite at the AIOStreams call
  boundary (one fmt.Sprintf).
- `extensions/stremio-custom-source/main.ts` — a full Stremio catalog/meta client in ~550 lines.
  What it proved: catalog pagination via `skip` extra works; `meta.videos[]` gives usable
  episode lists. What it exposed (all consequences of squeezing into `AL_BaseAnime`):
  - TV seasons get **flattened to absolute numbering** (`classifyVideos()` sorts season+episode and
    renumbers 1..N) — season structure is lost in the UI.
  - Movies are faked as `format: "MOVIE"` anime with `episodes: 1`.
  - `status` is hardcoded `FINISHED`; airing/continuing info lost.
  - Episode-id mappings are smuggled **base64-encoded inside `siteUrl`** for the plugin to recover.
  - Specials handling is unreliable ("Episode 0"/reversed order) and was disabled.
  These are the exact deficiencies the new vertical must not have.
- `extensions/aiostreams-torrent-provider/main.ts` — shows the stream-search call shape:
  `aiostreams.search(type /* 'movie'|'series' */, id /* 'tt123' or 'tmdb:123' */, season?, episode?)`,
  dedup by `infoHash || streamUrl || name`, and the module-level anime-id resolution cache.
- `extensions/aiostreams-plugin/stream/player.ts` — playback fan-out: URL-type results go to
  `videoCore.playStream(url, aniDBEpisode, anime)` (built-in player) or
  `playback.streamUsingMediaPlayer(...)`; p2p results go to `torrentstream.startStream({...})`.
  The new vertical replaces these plugin-API calls with its own handlers, but the branching logic
  (url vs p2p, Denshi-client discovery for nativeplayer) is the template.
- `packages/server/src/routes/api/search.ts` + `app.ts` — the Search API: `GET /api/v1/search`,
  query `{ type, id, requiredFields?, format? }`, basic-auth `uuid:password` or `encodedUserData`,
  gated by `enableSearchApi`. Response: `{ results: [...] }` stream objects (type `p2p|debrid|http|usenet|live`,
  `url`/`streamUrl`, `infoHash`, `filename`, `folderName`, `size`, `seeders`, `fileIdx`, parsed metadata).
  `lib/aiostreams.ts` (`parseManifestUrl`) shows how to derive `baseUrl/uuid/encryptedPassword` from a
  pasted manifest URL — replicate that parsing in Go so the Seanime setting is just "paste manifest URL".

### 2.2 AIOMetadata (cedya77/aiometadata) — reference implementation, not a dependency

A self-hostable Stremio metadata addon (Node/Express) that aggregates TMDB/TVDB/IMDb/TVmaze
(+ MAL/AniList/Kitsu/AniDB for anime) with cross-ID mapping and artwork selection. **We do not run
it, fork it, or talk to it.** It was studied for *how a mature movies/TV metadata layer is built*,
and these are the lessons adopted:

- **TMDB is the backbone** for movie/series metadata (AIOMetadata credits MrCanelas's TMDB addon as
  its groundwork; TMDB is its default provider for both types). Everything else (TVDB, TVmaze,
  Fanart) is enrichment we can skip in v1. → Seanime gets a **native TMDB client** (§3.2), the same
  way anime has native AniList/anizip clients.
- **Bring-your-own-API-keys as optional config**: AIOMetadata lets users supply personal
  TMDB/TVDB/etc. keys for rate-limit headroom while the hosted instance provides defaults. → same
  model: embedded default TMDB key, optional per-instance override in settings.
- **Cross-ID mapping matters**: its robust MAL↔TMDB↔TVDB↔IMDb mapping is what makes anime + Trakt
  interop work in the Stremio world. → we get the identical capability from data already in the
  repo (anizip mappings, §2.4) — no new mapping dataset needed.
- **Aggressive caching + poster proxying** keep it fast under rate limits. → our repository caches
  via the existing `filecache` (§3.2); TMDB image URLs are served directly by `image.tmdb.org`
  (no proxying needed at our scale).
- Its anime side (Jikan/MAL) is irrelevant to us — anime metadata stays AniList inside Seanime.

**TMDB API facts** (api.themoviedb.org/3, free key, generous limits ~50 req/s per IP):
`/trending/{movie|tv}/{day|week}`, `/movie/popular`, `/tv/popular`, `/movie/top_rated`,
`/tv/top_rated`, `/search/movie?query=`, `/search/tv?query=`,
`/movie/{id}?append_to_response=external_ids,credits`, `/tv/{id}?append_to_response=external_ids`
(returns the season list), `/tv/{id}/season/{n}` (episode list: number, name, overview, still,
air_date, runtime), `/discover/{movie|tv}?with_genres=`. `external_ids` yields `imdb_id` (movies
**and** shows) — the id AIOStreams' scrapers and Trakt both prefer. Images:
`https://image.tmdb.org/t/p/{w342|w780|original}{path}`.

### 2.3 Stremio-Kai (allecsc/Stremio-Kai)

A Windows Stremio build (fork of Zaarrg's stremio-community-v5) with a bundled MPV stack. Reviewed
for design ideas, **no code to reuse** (its value-add is MPV configs/shaders — Seanime/Denshi already
has a better player). Takeaways adopted:

- Its data model is instructive: anime and movies/TV coexist keyed by Stremio ids, and *behavior* is
  conditional (anime-only playback profiles activate when content is anime). Ours is the inverse —
  separate verticals — but its ratings UX (multi-source ratings on detail pages: IMDb/TMDB/Trakt/MAL)
  is worth mirroring: our detail page shows whatever ratings the addon meta carries (`imdbRating`),
  nothing more in v1.
- Trakt appears only as a ratings source there; it has no scrobbling. Nothing to copy for §4.

### 2.4 Trakt API facts (verified 2026-07)

- Base `https://api.trakt.tv`, headers: `Content-Type: application/json`,
  `trakt-api-version: 2`, `trakt-api-key: {client_id}`. User endpoints add `Authorization: Bearer`.
- **Device-code OAuth** (right flow for a headless server): `POST /oauth/device/code` → show
  `user_code` + `https://trakt.tv/activate` → poll `POST /oauth/device/token` (400 = still waiting)
  → tokens. Refresh via `POST /oauth/token` `grant_type=refresh_token`.
- **Access tokens expire in 24 h** (changed 2025-03-20; old docs say 90 days — ignore them). Refresh
  must be automatic and based on `expires_in`/`created_at`, not a hardcoded interval. Persist both
  tokens; a refresh failure requires re-auth (surface in settings UI, don't crash).
- Scrobble: `POST /scrobble/start|pause|stop` with `{ movie: {ids: {...}} }` or
  `{ show: {ids: {...}}, episode: {season, number} }` plus `progress` (float %). **`/stop` with
  progress ≥ 80 % records watched; < 80 % is stored as a pause** (resume point retrievable via
  `/sync/playback`). A watching status auto-expires; no keepalive needed.
- Accepted ids: movies `imdb`/`tmdb`/`trakt`/`slug`; shows `imdb`/`tmdb`/`tvdb`/`trakt`/`slug`;
  episodes by `{season, number}` under the show, or by their own ids. So AIOMetadata's `tt…`/`tmdb:`
  ids can be sent **verbatim** — no lookup round-trip needed for movies/TV.
- Rate limits: ~1000 GET / 5 min, POSTs limited to ~1/sec — a per-client 1 req/s throttle + 429
  `Retry-After` handling is sufficient.
- No VIP requirement for the API. User creates their own API app at trakt.tv/oauth/applications
  (redirect URI `urn:ietf:wg:oauth:2.0:oob`) and pastes client id + secret into Seanime settings —
  standard practice for self-hosted apps (Kometa does the same), keeps no shared secret in the repo.

**Anime → Trakt mapping (the hard part, solved with data we already have):**

- Trakt models anime as regular TVDB/TMDB-shaped shows (seasons ≠ AniList's per-cour entries).
  Needed per playback: show-level id (tvdb/tmdb/imdb) + **TVDB season & episode number** for the
  AniList episode being watched.
- `internal/api/anizip` already returns exactly this: `anizip.Episode` has `SeasonNumber`,
  `EpisodeNumber`, `AbsoluteEpisodeNumber`, `TvdbEid` (`internal/api/anizip/anizip.go:14-29`), and
  `anizip.Mappings` has `ThetvdbID`, `ImdbID`, `ThemoviedbID` (`anizip.go:32-45`). The generic
  `metadata.EpisodeMetadata` struct preserves `SeasonNumber`/`EpisodeNumber`/`TvdbId`
  (`internal/api/metadata/types.go:44-59`).
- Caveat: the **animap** primary provider hardcodes `ImdbId: ""`
  (`internal/api/metadata_provider/provider.go:200`); only the **anizip** fallback populates it.
  Therefore the Trakt mapper must **not** rely on whatever provider is configured — it calls
  `anizip.FetchAniZipMediaC("anilist", mediaId, cache)` directly at scrobble time (cached), takes
  show ids from `Mappings` and season/episode from `Episodes[strconv.Itoa(epNum)]`.
- AniList `format == MOVIE` entries scrobble as Trakt **movies** using `ImdbID`/`ThemoviedbID`.
- Specials (Seanime episode keys `S1…`) are skipped in v1 (season-0 mapping is unreliable).
- No mapping found → log at debug, skip silently. Never block or fail playback for a scrobble.

### 2.5 Codebase seams (from the 2026-07-06 surveys; anchors approximate)

**Reusable as-is (content-agnostic):**

- The entire byte-serving pipeline: `internal/directstream`'s `Stream` interface + open-state machine
  (`stream.go:87-230`), HTTP serving (`serve.go`, `httpstream.go`), `internal/videocore`,
  `internal/mkvparser`/`matroska`/`pgs`, `internal/mediastream` transcode.
- `internal/continuity`'s manager/bucket/trim machinery — but **not** its key space (below).
- Torrent extension interface: `hibiketorrent.AnimeProvider.Search(AnimeSearchOptions{Query})`
  honors a plain query string; `Media.Format` already normalizes to `"TV"|"MOVIE"`
  (`internal/extension/hibike/torrent/types.go:35-121`). Not needed for v1 (AIOStreams covers
  streams), but available for a later "raw torrent search" tab without interface changes.
- Codegen: doc-comment driven (`@route /api/v1/... [METHOD]` etc.), type prefix = Go package name →
  a new `internal/media` package auto-yields `Media_*` TS types. No tooling changes.
- Settings gate pattern: `LibrarySettings.EnableManga` (`internal/database/models/models.go:117`,
  embedded gorm settings) + `db.AutoMigrate(...)` list (`internal/database/db/db.go:87+`).

**Anime-coupled (needs a parallel, with the exact chokepoints):**

- `internal/directstream/manager.go:33-87` holds `animeCollection` + `animeCache`; **every** play
  entry (`PlayDebridStream` `debridstream.go:44-75`, `PlayUrlStream` `urlstream.go:41-70`,
  `PlayTorrentStream`, `PlayLocalFile`) requires `Media *anilist.BaseAnime` + `AnidbEpisode string`
  and builds `anime.NewEpisodeCollection(...)` → `FindEpisodeByAniDB(...)`. **There is no
  content-agnostic play entry today.** §3.4 adds one.
- `internal/continuity/history.go:28-51`: `WatchHistory = map[int]*WatchHistoryItem`, int-keyed —
  an AniList id and a foreign int id would collide. Movies/TV get **separate buckets** with string
  keys; anime buckets untouched.
- `internal/platforms/platform/platform.go:8-62`: the `Platform` interface is the AniList API
  surface (`GetAnime(...) *anilist.BaseAnime`, `GetAnilistClient()`, …). **A Trakt platform cannot
  and should not implement it.** Trakt is a sidecar service invoked *alongside* platform calls.
- Progress chokepoint: `playbackmanager/progress_tracking.go:641-720` `updateProgress()` →
  `platformRef.Get().UpdateEntryProgress(...)` (:704) — the single place anime progress syncs; the
  natural Trakt fan-out point. The native-player path flows through `mediacore.Coordinator`
  (`updateContinuity` / `SetupSharedEffects` in `internal/mediacore/mediacore.go`) — verify which of
  the two paths fires `updateProgress` for VideoCore playback before wiring (v3.9 merged this; both
  MPV and VideoCore completions are expected to reach `updateProgress`, confirm with a log line).
- Server-side continue-watching (`internal/library/anime/collection.go:349-427`) is built from the
  AniList CURRENT list — unusable for movies/TV. The durable `_lw` last-watched store
  (`continuity/history.go:93-111`, per-user buckets `manager.go:52-71`) is the right model to copy.
- Discord RPC hardcodes `https://anilist.co/anime/%d` (`internal/discordrpc/presence/presence.go:318`);
  nakama watch-party identity is `MediaId int` (`internal/nakama/watch_party.go:186,223,482`). Both
  are **out of scope v1** for movies/TV (presence off, nakama rejected with a toast) — see §3.4.
- Frontend: `VideoCore_VideoPlaybackInfo` is content-agnostic except `media?: AL_BaseAnime` +
  `episode?: Anime_Episode`; the AL coupling inside `video-core.tsx` is exactly 4 spots — title
  display (:643), continuity save by media id (:1055), continuity seek-restore by episode
  (:1640-1641), AniSkip by format (:1846,1859). All are optional-chained; a nil `media` largely
  no-ops, but each must be explicitly handled (§3.6).
- `MediaEntryCard<T extends "anime" | "manga">` (`_features/media/_components/media-entry-card.tsx:66-79`)
  is a **closed two-literal generic** threaded through many call sites. **Decision: do not widen it.**
  Movies/TV get their own simple card (§3.6).
- Search (`search/_lib/handle-advanced-search.ts:10-119`) is two parallel `useInfiniteQuery`s with
  hardcoded `type === "anime"` branches; Discover tabs are `__discord_pageTypeAtom:
  atom<"anime"|"schedule"|"manga">` + `enableManga`-gated `StaticTabs`
  (`discover/page.tsx:52-65`). Both extend by addition, not modification.
- Beware naming: Discover already has a "Trending Movies" row — **anime** movies from AniList
  (`discover-trending-movies.tsx`, `handle-discover-queries.ts:123-132`). Label the new tabs
  "Movies" / "TV Shows" and consider renaming that anime row to "Trending Anime Movies" for clarity
  (allowed: it's a string label, not a behavior change).

---

## 3. Design

### 3.1 Identity

```go
// internal/media/types.go
type MediaType string // "movie" | "tv"

type MediaKey struct {
    Type   MediaType `json:"type"`
    TmdbID int       `json:"tmdbId"`
}
func (k MediaKey) String() string { return fmt.Sprintf("%s/tmdb:%d", k.Type, k.TmdbID) } // canonical: cache/continuity/DB key
func ParseMediaKey(s string) (MediaKey, error)
```

- TMDB ids are the native id space (metadata is TMDB), and the canonical string (`movie/tmdb:603`,
  `tv/tmdb:1396`) is the **only** key form used in continuity buckets, DB rows, filecache, and API
  paths. String-typed and namespaced ⇒ structurally incapable of colliding with AniList ints even
  though TMDB ids are numeric underneath.
- `MediaDetails` carries `ImdbID string` (from TMDB `external_ids`) — fetched once with details,
  snapshotted into `MediaProgress` rows so streams/Trakt never need a second lookup.
- Episode addressing for TV: `MediaKey` + `Season int` + `Episode int` (TMDB's real numbering).
- **AIOStreams boundary**: query with the IMDb id when known (`tt123` / `tt123:2:5` — best scraper
  coverage), fall back to `tmdb:603` composite form otherwise (AIOStreams resolves both).
- **Trakt boundary**: `ids.tmdb` directly (movies and shows both accept it); include `ids.imdb`
  when snapshotted. No lookup round-trip ever needed.

### 3.2 Backend package: `internal/media/`

```
internal/media/
  types.go            MediaKey, MediaItem, MediaDetails, Season, Episode, StreamCandidate, dtos
  tmdb.go             native TMDB v3 client: Trending, Popular, TopRated, Search, MovieDetails,
                      TVDetails, SeasonEpisodes, DiscoverByGenre — plain REST GETs + structs
  repository.go       the vertical's façade: DiscoverRows(), Search(q,type), Details(key),
                      SeasonEpisodes(key,n); caching via filecache (24h details / 3h discover rows / 24h seasons)
  aiostreams.go       Search API client: ParseManifestURL(url) → {base,uuid,password}; GetStreams(key, imdbID, season, episode) → []StreamCandidate
  tracking.go         local progress: MediaProgress gorm model CRUD + ContinueWatching() assembly
  continuity.go       thin wrapper over a continuity-style store with string keys (separate buckets)
```

- `tmdb.go` follows the shape of the existing native API clients (`internal/api/anizip/anizip.go` is
  the template: plain structs + one fetch func + package cache). API key resolution: settings
  override → embedded default constant (`internal/constants` or a private `defaultTMDBKey` in the
  package). Send as `Authorization: Bearer` (v4 token) or `?api_key=` (v3) depending on key shape.
- Discover rows are **fixed, code-defined** (no dynamic catalog registry): Trending Movies,
  Trending TV, Popular, Top Rated (+ genre rows later via `/discover`). Each row = one TMDB endpoint
  + one cache entry; the handler exposes them behind stable row ids (`trending`, `popular`,
  `top_rated`) so the frontend stays dumb.
- `MediaItem` (list shape): key, title, poster, backdrop, year, rating (TMDB `vote_average`),
  description, genres. `MediaDetails` adds runtime, status, cast (top-billed from `credits`),
  `ImdbID`, `Seasons []Season{Number, Name, EpisodeCount}`; episodes fetched lazily per season via
  `/tv/{id}/season/{n}` (`Episode{Number, Title, Overview, Still, AirDate, Runtime}`). **Preserve
  real season/episode numbers** — no absolute flattening (the prototype's central sin). Season 0
  ("Specials") shown last.
- AIOStreams manifest URL resolution order: `MediaSettings.AIOStreamsManifestURL` override → the
  installed AIOStreams extension's saved user config (locate where extension user preferences are
  persisted — `internal/extension_repo`/plugin data — and read the `manifestUrl` preference of the
  `aiostreams-torrent-provider` / plugin extension). Zero re-entry when the extension is already
  set up, which it is on every current deployment.
- `StreamCandidate`: `{Type: "url"|"p2p", Name, Title, URL, InfoHash, FileIdx, Size, Seeders, Quality…}` —
  mapped from the Search API response; keep AIOStreams' result **order** (user's AIOStreams config
  owns ranking — consistent with the standing "quality over speed" rule for anime).
- `MediaProgress` gorm model (add to `models.go` + the `AutoMigrate` list in `db.go`):

```go
type MediaProgress struct {
    BaseModel
    UserID     uint   `gorm:"index:idx_media_progress,unique"`
    MediaKey   string `gorm:"index:idx_media_progress,unique"` // "movie/tt…" | "tv/tmdb:…"
    Season     int    `gorm:"index:idx_media_progress,unique"` // 0 for movies
    Episode    int    `gorm:"index:idx_media_progress,unique"` // 0 for movies
    // display snapshot so continue-watching needs no addon round-trip:
    Title      string
    Poster     string
    TotalSeasonEpisodes int   // for "next episode" computation
    CurrentTime float64       // seconds
    Duration    float64
    Watched     bool          // set at >=90% like anime's completion threshold
    TimeUpdated time.Time
}
```

- `ContinueWatching(userID)` returns the newest row per MediaKey, mapped to: movies → resume card;
  TV → resume card if unfinished episode, else "next episode" card (episode+1 within season, else
  season+1 episode 1, else nothing — no aired-date logic in v1; addon `videos` with future
  `released` dates are filtered out when computing "next").
- Trakt fan-out for movies/TV lives here: `tracking.SetProgress(...)` calls
  `trakt.ScrobbleStop(...)` (async, fire-and-forget goroutine) when crossing the watched threshold.

### 3.3 Handlers, routes, settings

New file `internal/handlers/media.go` (+ `media_trakt.go` for auth endpoints), group registered in
`routes.go` like the continuity group (`routes.go:544-547`), all gated server-side on the flag:

| Route | Handler | Notes |
|---|---|---|
| `GET  /api/v1/media/discover/:type` | fixed rows `{rowId, title, items[]}` (trending/popular/top-rated) | one call per tab, server assembles rows |
| `GET  /api/v1/media/search?type=&q=&page=` | `[]Media_MediaItem` | TMDB `/search/{movie\|tv}` |
| `GET  /api/v1/media/details/:type/:tmdbId` | `Media_MediaDetails` (season list inline, imdb id included) | |
| `GET  /api/v1/media/episodes/:tmdbId/:season` | `[]Media_Episode` | lazy per-season fetch |
| `GET  /api/v1/media/streams/:type/:tmdbId?season=&episode=` | `[]Media_StreamCandidate` | AIOStreams Search API proxy (prefers imdb id) |
| `POST /api/v1/media/play` | `{key, season, episode, candidate, clientId?}` → starts playback (§3.4) | |
| `GET  /api/v1/media/continue-watching` | `[]Media_ContinueWatchingItem` | per-user |
| `POST /api/v1/media/progress` | manual mark watched/unwatched | |
| `GET  /api/v1/trakt/device-code` / `POST /api/v1/trakt/poll` / `DELETE /api/v1/trakt/logout` | device auth dance | per-user |

Codegen: annotate with the standard doc comments; run codegen; new `Media_*`/`Trakt_*` types +
`MEDIA`/`TRAKT` endpoint groups appear in `seanime-web/src/api/generated/`. Hand-write
`seanime-web/src/api/hooks/media.hooks.ts` + `trakt.hooks.ts` following `anime_entries.hooks.ts`.

Settings (three independent toggles, all default **off**):

- `LibrarySettings.EnableMediaVertical bool` — master gate for movies/TV (nav, discover tabs,
  search types, routes). This is the **only** switch required to use the feature.
- `MediaSettings` new embedded struct, all fields **optional**: `TmdbApiKey string` (personal key
  for rate-limit headroom; empty = embedded default — the AIOMetadata bring-your-own-keys model),
  `AIOStreamsManifestURL string` (override; empty = auto-resolved from the installed AIOStreams
  extension per §3.2). Admin-owned (shared infra plane, consistent with the profile-support model).
- `TraktSettings` new embedded struct: `Enabled bool`, `ClientID string`, `ClientSecret string`
  (admin-owned app credentials), `ScrobbleAnime bool`, `ScrobbleMedia bool`. Tokens are **not**
  settings — per-user `Trakt` model (§4).

### 3.4 Playback path (the one genuinely new piece of plumbing)

Add to `internal/directstream` a content-agnostic entry point mirroring `PlayUrlStream`/
`PlayDebridStream` but **without** `anilist.BaseAnime` / `AnidbEpisode` / `anime.EpisodeCollection`:

```go
type PlayGenericStreamOptions struct {
    ClientId     string
    URL          string            // pre-resolved (AIOStreams url-type candidate)
    // p2p candidates first resolve through the existing debrid/torrentstream primitives:
    //   debrid: provider.GetTorrentStreamUrl(hash…) — content-agnostic already
    //   torrentstream: Client.addTorrentMagnet(magnet) + file selection by FileIdx
    Display      GenericDisplayInfo // {Key media.MediaKey, Title, EpisodeLabel, Poster, Backdrop}
    ContinuityKey string            // "movie/tt…" or "tv/tmdb:…/s2e5"
}
func (m *Manager) PlayGenericStream(ctx context.Context, opts PlayGenericStreamOptions) error
```

Implementation notes (each is a known coupling from §2.5 — handle explicitly, do not discover them
at runtime):

1. Reuse the existing `httpBaseStream` open-state machine (`BeginOpen`/`CloseOpen`/`AbortOpen`) and
   serving layer untouched. The stream struct gets `Display`/`ContinuityKey` instead of
   media/episode; where the current streams populate `PlaybackInfo.Media`/`.Episode`, the generic
   stream leaves them nil and fills new optional fields `PlaybackInfo.Generic *GenericDisplayInfo`.
2. `mediacore.Coordinator` continuity hooks (`restoreContinuity`/`updateContinuity`): route by key —
   if the session has a `ContinuityKey`, read/write the **media** continuity store
   (`internal/media/continuity.go`) instead of the anime one. Anime sessions unchanged.
3. Discord presence: skip entirely for generic sessions in v1 (`presence.go` never receives an
   activity). A `MediaActivity` with a themoviedb.org URL is a later nicety.
4. Nakama: generic sessions are not shareable in v1 — if a watch-party is active, return a clear
   error ("Movies/TV can't be streamed to a watch room yet") rather than sending a bogus int id.
5. Progress: on the same cadence the anime path saves continuity, also upsert `MediaProgress`
   (CurrentTime/Duration); on completion threshold set `Watched` + Trakt stop-scrobble.
6. Denshi/MpvCore: the client side is the same `NATIVE_PLAYER` websocket protocol — `OpenAndAwait`
   carries the playback info; no Denshi-side protocol change is expected, but **any web change
   reaching Denshi requires a Denshi rebuild** (bundled UI), and MpvCore playback is testable only
   via `scripts/build-denshi-local.sh --installer` (browser has no MpvCore; unpacked builds have the
   known black-video bug).

### 3.5 Trakt package: `internal/trakt/`

```
internal/trakt/
  client.go     raw API: DeviceCode(), PollToken(), Refresh(), ScrobbleStop(payload), throttle (1 rps) + 429 retry
  auth.go       token lifecycle: per-user tokens, auto-refresh when now > created_at+expires_in-300s, re-auth surfacing
  scrobbler.go  the service the rest of the app calls (see interface below)
  mapping.go    anime → Trakt payload via anizip (cached); media → payload via MediaKey (trivial)
  queue.go      TraktScrobbleQueueItem gorm model + retry loop (offline/API-down resilience)
```

```go
// The only surface the rest of the app sees. All methods non-blocking (enqueue + goroutine), never error to callers.
type Scrobbler interface {
    // anime path — called from playbackmanager.updateProgress (progress_tracking.go:704 vicinity),
    // AFTER the AniList update, gated on TraktSettings.Enabled && ScrobbleAnime:
    ScrobbleAnimeEpisode(userID uint, mediaId int, episodeNum int, progressPct float64)
    // media path — called from internal/media/tracking.go, gated on ScrobbleMedia:
    ScrobbleMedia(userID uint, key media.MediaKey, season, episode int, progressPct float64)
}
```

- **v1 is stop-only scrobbling**: one `POST /scrobble/stop` when Seanime marks the item complete
  (progress ≥ the app's ~90 % threshold → Trakt sees ≥ 80 % → records watched). No start/pause —
  no session-state to babysit, an interrupted watch simply doesn't scrobble (matches how the AniList
  auto-sync already behaves). Start/pause (live "watching now" status + cross-device resume) is a
  polish phase.
- Token storage: new `Trakt` gorm model, one row per user (template: the existing `Mal` model —
  same file, same pattern): `UserID`, `AccessToken`, `RefreshToken`, `CreatedAtUnix`, `ExpiresIn`,
  `Username`. Add to AutoMigrate.
- Queue: on any send failure (network, 5xx, expired-and-refresh-failed), persist the payload row and
  retry with backoff (next app start + hourly ticker). Cap retries (e.g. 7 days) then drop with a log.
- Multi-user: every scrobble call carries `userID`; no token → silent no-op. Device auth endpoints
  operate on the session user.
- Mapping cache: `anizip` responses cached via the package's existing `Cache`; one fetch per
  (media, session) is plenty.

### 3.6 Frontend

New route tree `seanime-web/src/app/(main)/media/` (mirrors the established "one directory pair per
content type" pattern the offline feature already uses):

- **Nav**: `top-menu.tsx` gets `Movies` and `TV Shows` entries using the exact `enableManga`
  conditional-spread idiom (:40-45), gated on `serverStatus.settings.library.enableMediaVertical`.
- **Discover**: extend `__discord_pageTypeAtom` union with `"movies" | "tv"` + two `StaticTabs`
  entries (gated). Each tab renders the fixed rows from `GET /media/discover/:type` — one
  `Carousel` row per row-id, cards from the new `MediaItemCard`. Anime/manga/schedule tab code
  untouched.
- **Search**: add `"movie" | "tv"` to `__advancedSearch_paramsAtom.type`; in
  `handle-advanced-search.ts` add one new `useInfiniteQuery` hitting `GET /media/search`, enabled
  only for the new types; branch in `advanced-search-list.tsx` renders `MediaItemCard`. The two
  existing queries/branches unchanged.
- **Cards**: new `MediaItemCard` (poster, title, year, rating, progress bar) — deliberately **not**
  `MediaEntryCard`; do not widen its closed generic. Reuse the presentational atoms it composes
  (image container, hover popup styles) where trivially importable.
- **Detail pages**: `/media/[type]/[id]/page.tsx` — movie: hero + Play button + stream-candidate
  drawer; TV: hero + season selector + episode grid (real season numbers) + per-episode Play. The
  stream drawer lists `Media_StreamCandidate`s in AIOStreams order with a "best" default; selecting
  calls `POST /media/play`. Copy the `currentView` tab state-machine shape from
  `anime-entry-page.tsx` only if it earns its keep — a movie page is simple enough without it.
- **Player**: no new player. `PlaybackInfo.media`/`episode` are nil for generic sessions — handle
  the four `video-core.tsx` touchpoints: title section reads `playbackInfo.generic` when `media` is
  nil (:643); continuity save/restore uses the generic continuity endpoints keyed by
  `ContinuityKey` (:1055, :1640); AniSkip no-ops when `media` is nil (:1846,1859 — verify
  optional-chaining actually short-circuits, add explicit guards).
- **Home / continue watching**: a new self-contained `MediaContinueWatchingRow` (feeds from
  `/media/continue-watching`) rendered below the anime continue-watching section, only when the
  flag is on and the row is non-empty. Do **not** merge into `Anime_Episode`-typed components or
  touch `sortContinueWatchingEntries`/the `_lw` logic in `filtering.ts`.
- **Settings**: new `media-settings.tsx` container (the enable flag + the two optional fields:
  personal TMDB key, AIOStreams URL override — clearly labeled "optional", admin-only) and
  `trakt-settings.tsx` (enable, client id/secret [admin], per-user "Connect Trakt" device-code
  modal: shows `user_code`, link, polls `POST /trakt/poll`, shows connected username + disconnect).
  Wire both into `settings/page.tsx`'s submit payload + default hydration (the two-touchpoint
  pattern, see `enableManga` at :377/:563).
- **Clients**: Denshi — any of this reaching Denshi needs a rebuild (tag flow); Tenji — hand-sync
  only the `Media_*`/`Trakt_*` generated types when/if Tenji grows the feature (not v1; the routes
  404 gracefully for old clients since everything is new+flagged).

---

## 4. Phases (implement in order; each independently shippable behind the flag)

**P1 — Metadata + browse (no playback).**
Create `internal/media` (types, TMDB client, repository, discover/search/details/episodes
handlers), settings structs + flags + migration, codegen, nav entries, Discover tabs, Search types,
detail pages (read-only, Play disabled). *Accept:* flip the flag on — **with no other
configuration** — and Movies/TV tabs browse trending/popular, search works, a TV detail page shows
correct seasons with lazy episode lists (spot-check a multi-season show, e.g. Breaking Bad TMDB
`1396` — 5 seasons, not 62 absolute episodes), a personal TMDB key entered in settings is used in
place of the embedded default. With flag off: UI and API byte-identical (verify: no new routes
respond, no nav entries).

**P2 — Streams + playback + local tracking.**
`aiostreams.go`, streams handler, `PlayGenericStream` in directstream (+ the mediacore continuity
routing, discord/nakama guards), `MediaProgress` + continuity buckets + continue-watching handler,
stream drawer + play buttons + home row, video-core nil-media touchpoints. *Accept:* a movie plays
end-to-end in Denshi (installer build) via an AIOStreams url-type candidate; a p2p candidate plays
via debrid; kill the app mid-movie → home shows resume card → resume seeks correctly; finishing a
TV episode surfaces "next episode"; **anime playback regression pass** (one debrid stream, one
library file — progress + continue watching unchanged).

**P3 — Trakt for movies/TV.**
`internal/trakt` (client/auth/scrobbler/queue), `Trakt` + queue models, device-auth handlers +
settings UI, hook in `media/tracking.go`. *Accept:* connect account via device code; finish a movie
→ appears in trakt.tv history within seconds; finish a TV episode → correct show/season/episode;
kill network mid-scrobble → queued → delivered on retry; token older than 24 h auto-refreshes.

**P4 — Trakt for anime.**
`mapping.go` anizip path, hook after `UpdateEntryProgress` in `progress_tracking.go` (both MPV and
VideoCore completion paths verified), `ScrobbleAnime` toggle. *Accept:* finish an anime episode
(AniList-tracked) → correct Trakt show/season/episode for (a) a season-1 show, (b) a later-cour
entry (e.g. an "Xth Season" AniList entry mapping into TVDB season N), (c) an anime movie → Trakt
movie. AniList sync behavior unchanged; anime with no anizip mapping logs + skips.

**P5 — Polish (each optional, pick by appetite).**
Scrobble start/pause (live status), `/sync/playback` cross-device resume import, Trakt history
backfill import, Discord presence for movies/TV, nakama watch-party support, genre/catalog filter
UI, watchlist, offline snapshot of media progress, Tenji support.

---

## 5. Do-not-touch list (regression guards)

- `internal/library/**` (scan/collection), `internal/platforms/**`, `internal/api/metadata*` —
  read-only consumers only (Trakt mapper *reads* anizip; changes none of it).
- `internal/continuity` anime buckets and key types — media continuity is separate buckets; do not
  migrate or re-key existing data.
- `sortContinueWatchingEntries` / `getUpNextBoostDate` in `seanime-web/src/lib/helpers/filtering.ts`
  (fresh `_lw` work, live on prod).
- `MediaEntryCard`'s generic and every `type === "anime" ? … : …` call site.
- The four modified-in-place hook points get the **smallest possible diff**: `progress_tracking.go`
  (one call after UpdateEntryProgress), `video-core.tsx` (guards at 4 anchors), `top-menu.tsx` /
  discover atoms / search atoms (additive entries), `routes.go` (one group).
- Anything in the anime torrent/debrid finder paths (`internal/debrid/client/stream.go`,
  `finder.go`, `torrentstream/**`) — the media vertical does not enter them; p2p candidates use the
  provider-level primitives directly.

## 6. Open items for the user (not blockers — defaults chosen)

1. A TMDB API key to embed as the default (free: themoviedb.org → account → API). One-time,
   pre-implementation. Optionally also generate a personal one later for the settings override —
   same thing, the split only matters if the fork is ever shared.
2. Trakt app credentials: create at trakt.tv/oauth/applications (OOB redirect), paste into settings.
3. Naming of the existing anime "Trending Movies" Discover row (suggest "Trending Anime Movies").
4. ~~Separate nav entries vs one "Cinema" entry~~ — **resolved 2026-07-06**: Movies and TV are
   separate top-level entries, peers of Anime (constraint 5).
