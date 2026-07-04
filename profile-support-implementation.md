# Profile / multi-user support — implementation plan (Option A)

Status: implementation plan, grounded in a code read. No code written yet.
Companion to `profile-support.md` (the why/what). This doc is the how.

Decision locked: **Option A** — one backend, many users; admin owns the shared
content/infrastructure plane, each user owns their identity/state plane.

---

## 1. What the code actually looks like (research findings)

Every finding below is load-bearing for the design. File references are exact.

### 1.1 The whole backend is one user's world

`core.App` (`internal/core/app.go`) is a single-user state machine:

- `App.user *user.User` — **the** current user (one field, `app.go:156`).
- `App.Settings *models.Settings` — **the** settings (`app.go:140`), one row at
  `id = 1`, cached in package global `db.CurrSettings`
  (`internal/database/db/settings.go:10,40`).
- `App.AnilistPlatformRef *util.Ref[platform.Platform]` — **one** swappable
  AniList platform (`app.go:78`), mutated globally between anilist / simulated /
  offline based on auth state.
- `db.GetAccount()` does `gormdb.Last(&acc)` into package global `accountCache`
  (`internal/database/db/account.go:32-49`) → one AniList account/token.

### 1.2 Modules don't just *read* the user — they *cache its collection*

This is the crux that rules out a cheap fix. `App.RefreshAnimeCollection()`
(`internal/core/anilist.go:285-321`) **pushes one user's AniList collection into
the modules**:

```
a.PlaybackManager.SetAnimeCollection(ret)
a.AutoDownloader.SetAnimeCollection(ret)
a.LocalManager.SetAnimeCollection(ret)
a.DirectStreamManager.SetAnimeCollection(ret)
a.LibraryExplorer.SetAnimeCollection(ret)
a.AutoScanner.SetAnimeCollection(ret)
```

So PlaybackManager, DirectStreamManager, LibraryExplorer, etc. each hold **one
user's collection in a field**. They are constructed once in
`initModulesOnce()` (`internal/core/modules.go:49-347`) with the single
`a.AnilistPlatformRef` and the single `a.WSEventManager`. Multi-user can't share
these instances — they are per-user state by construction.

### 1.3 The WS event bus is broadcast, but targeted send already exists

`events.WSEventManager` (`internal/events/websocket.go`):

- `WSConn{ID, Platform, Conn}` (`:77-81`) — **no user/account field**.
- `SendEvent(t, payload)` (`:168`) writes to **all** conns. 258 call sites.
- **But** `SendEventTo(clientId, ...)` (`:203`) already targets one client, and
  the WS handler already assigns each connection a `clientId` from a query param
  (`internal/handlers/websocket.go:40,60,67`).

So the primitive for per-recipient delivery exists. What's missing is a
`WSConn.UserID` and a `SendEventToUser`. The 258 broadcast calls are mostly made
*from inside the per-user modules* (1.2), so once those modules are per-user and
capture their owner's id, their sends become per-user almost for free.

### 1.4 Auth is one shared password, not an identity

`OptionalAuthMiddleware` (`internal/handlers/server_auth_middleware.go`): the
`X-Seanime-Token` header must equal `App.ServerPasswordHash`
(`app.go:161,216`, SHA-256 of `server.password`). It authorizes *the server*; it
never resolves *who* is calling. The WS upgrade uses the same single hash as a
query `token` (`websocket.go:27-38`). There is no user table, no session, no
per-user credential anywhere.

### 1.5 Login flow today

`LoginToAnilist(token)` (`internal/core/anilist.go:176-227`): validate token →
`UpsertAccount{ID:1}` → swap the global platform → `InitOrRefreshAnilistData()`
→ `InitOrRefreshModules()`. One account row, clobbered on each login.

### 1.6 Settings-dependent modules rebuild centrally

`InitOrRefreshModules()` (`modules.go:369-633`) reads `GetSettings()` and
reconfigures torrent client, media player, scanner, continuity, discord, nakama.
This is the single rebuild choke point — useful: per-user session construction
can hang off an analogous per-user init.

### 1.7 Frontend is driven by one status blob

`serverStatusAtom` carries `user`, `settings`, `debridSettings`,
`torrentstreamSettings` (`use-server-status.ts`). `useCurrentUser()` reads
`serverStatus.user`. The settings page is one monolithic tabbed `Form` rendering
every section (`settings/page.tsx` imports `DebridSettings`,
`MediastreamSettings`, `TorrentstreamSettings`, `ServerSettings`, …). Auth is a
single password in `serverAuthTokenAtom` sent as `X-Seanime-Token`.

---

## 2. Central design decision: per-user `UserSession`, not per-call threading

Two ways to multi-tenant the identity plane:

- **(rejected) Thread a `userID`/platform parameter through every method** that
  touches the user. Touches ~58 `GetSettings` + the playback/stream call graph +
  258 event sites. Enormous surface, and it fights 1.2 (the modules cache
  collection in fields, so a per-call param still has nowhere coherent to live).
- **(chosen) A `UserSession` object per logged-in user** that *bundles* the
  stateful per-user modules, capturing the owner's id once. The expensive shared
  engines are injected by reference. Handlers resolve
  `sess := app.SessionFor(userID)` and call into it.

`UserSession` (new, `internal/core/session.go` or `internal/usersession/`):

```
type UserSession struct {
    UserID    uint
    User      *user.User                 // their AniList viewer + token
    Platform  *util.Ref[platform.Platform] // their own AniList client/token
    Playback  *playbackmanager.PlaybackManager
    Stream    *directstream.Manager
    Video     *videocore.VideoCore
    Native    *nativeplayer.NativePlayer
    Explorer  *library_explorer.LibraryExplorer
    Presence  *discordrpc_presence.Presence // client-side; see §6
    collection *anilist.AnimeCollection      // was App-global, now per-session
}
```

The App keeps `sessions *result.Map[uint, *UserSession]` and the shared engines.
Each session's modules are constructed with the **shared** engines + the
session's own platform + a `userID` so their `SendEvent` becomes
`SendEventToUser(userID, …)`.

**Why this is the lazy-correct cut:** it matches the existing construction shape
(`initModulesOnce` already builds exactly these modules from refs) — we're
parameterizing that block by user, not rewriting call graphs. And it draws the
shared/per-user line exactly where the code already separates "engine" from
"collection holder."

### 2.1 Not everything per-user needs an *instance*

Distinguish two kinds of per-user module:

- **Stateful / event-emitting** (cache collection, run background playback loops,
  emit user-facing events): PlaybackManager, DirectStreamManager, VideoCore,
  NativePlayer, LibraryExplorer → **per-session instance**.
- **Request-driven DB CRUD** (no in-memory user state, no background emit):
  Continuity, Playlist, auto-select profiles, theme → **keep the singleton, add
  `user_id` to the queries**. Far cheaper.

This halves the per-instance surface.

---

## 3. Module classification (the work map)

| Module | Plane | Action |
|---|---|---|
| Torrent client repo (qbit/transmission/builtin) | **Shared/admin** | unchanged singleton |
| Torrent **engine** (anacrolix client: bytes/disk/peers, holds N torrents) | **Shared/admin** | unchanged; holds every user's active torrent at once |
| Torrentstream **session** (`currentTorrent`/`currentFile`/`previousStreamOptions`, finder, serve binding) | **Per-user** | split out of the repo into the session (§3A) |
| DebridClientRepository — account/HTTP client | **Shared/admin** | unchanged; add concurrency (§7) |
| DebridClientRepository — stream selection/serve/tracking | **Per-user** | per-session (§3A) |
| MediastreamRepository (transcoder pool) | **Shared/admin** | unchanged; add concurrency (§7); per-stream output already keyed |
| TorrentRepository (search), MetadataProvider, ExtensionRepo/Bank, FileCacher, Updater, Report | **Shared** | unchanged |
| AutoScanner, AutoDownloader, library watcher | **Admin** | operate on shared library; admin-only (§4.4) |
| MangaRepository | **Shared content** | per-user *progress* via `user_id` queries |
| AniList Platform (`AnilistPlatformRef`) | **Per-user** | one per session, own token |
| PlaybackManager, DirectStreamManager, VideoCore, NativePlayer, LibraryExplorer | **Per-user** | per-session instance (§2.1) |
| ContinuityManager, PlaylistManager | **Per-user data** | keep singleton, add `user_id` (§2.1) |
| DiscordPresence | **Per-user, client-side** | §6 |
| LocalManager (offline snapshot) | **Deferred** | single-user offline only; not multi-tenant in v1 |
| NakamaManager | **Replace for same-backend** | §5 |

---

## 3A. The streaming pipeline: one active stream *per user*

Required behavior (from the constraint): the existing "single active stream that
cancels the old one and follows the **last** requesting client" must be kept —
but **scoped per user**. If `clin` starts a stream from a second device, it
re-targets clin's clients (today's behavior). If `josh` starts one while clin is
watching, josh gets a **fully independent** selection → streaming → tracking
pipeline. Two users feel like two private instances.

### 3A.1 Why this isn't automatic with §3

Today the whole pipeline is a **single global session**, verified in code:

- `directstream.Manager` holds singular `currentStream`, `currentPlaybackId`,
  `currentPlaybackClient`, `currentStreamEpisodeCollection`, `animeCollection`
  (`internal/directstream/manager.go:53-69`). It is the central coordinator and
  tracks exactly one stream + one bound client.
- `torrentstream` serves the one global `client.currentTorrent` / `currentFile`
  (`internal/torrentstream/handler.go:ServeHTTP`); the stream URL carries no
  per-stream key. The repo also holds a single `currentTorrent` and
  `previousStreamOptions` (`repository.go:61`, `stream.go`).
- "Cancel/transfer to last client" = a new `StartStream` calls `StopStream` on
  the one current torrent and rebinds `currentPlaybackClient`
  (`torrentstream/stream.go:144-161`, `playback.go:60`). Events are already
  filtered by `ClientId` (`playback.go:101`), and `previousStreamOptions` is the
  "reopen on the other device" mechanism.

So the single-active-stream state is **server-global**, not user-global.

### 3A.2 The cut: shared engine, per-user session

Split each streaming subsystem along **engine vs session**:

| Stays SHARED (one, admin/infra) | Becomes PER-USER (in `UserSession`) |
|---|---|
| Torrent engine (anacrolix client): downloads/seeds/disk/peers, can hold **many** torrents at once | The active-stream pointer + selection: `currentTorrent`, `currentFile`, `currentTorrentStatus`, `previousStreamOptions`, the finder run, progress tracking |
| Debrid account + HTTP client, rate budget | Which debrid torrent is selected for *this* user's playback, its serve handle, tracking |
| Transcoder process pool / ffmpeg | (already per-output) the user's transcode session |
| `directstream.Manager` engine plumbing | `currentStream` / `currentPlaybackId` / `currentPlaybackClient` / episode + anime collection — **already in the session bundle (§2)** |

Concretely: `torrentstream.Repository`'s *session layer* (currentTorrent +
finder + serve binding) moves into `UserSession`, constructed with the **shared**
torrent engine injected by reference. The engine already supports N concurrent
torrents — only the "current pointer + serving + tracking" was singular. Each
user keeps their **own** `currentTorrent` + `previousStreamOptions`, so
cancel/transfer/reopen behaves exactly as today but bounded to that user's
clients. `directstream.Manager` is already per-session in §2, which gives each
user their own `currentPlaybackClient` for free.

### 3A.3 Serve URLs must carry a per-stream key

The blocking detail: today `/api/v1/torrentstream/stream` (and the directstream
serve route) resolve the **one** global `currentTorrent`/`currentStream`. With
two users streaming different files, `http.ServeContent` would collide. The serve
endpoints must resolve **whose** stream:

- Mint a per-stream/per-session token when a stream starts; the player URL
  becomes `…/stream?token=<streamId>` (the HMAC query-param plumbing already
  exists — `directstream` is constructed with an `HMACTokenFunc` in
  `modules.go:212`).
- `ServeHTTP` looks up the session/stream by that token instead of reading a
  global `currentTorrent`. Same for `directstream/serve.go`.

This is the one genuinely new wiring item the streaming split forces, beyond
moving state into the session.

### 3A.4 Concurrency falls out of this

Once streams are per-user, §7's limits become the natural backstop: the shared
torrent engine, debrid budget, and transcoder are where N users actually
contend. Size the semaphores there; the per-user session is what makes "N
independent streams" a coherent thing to limit in the first place.

---

## 4. Data model & settings

### 4.1 New tables (gorm `AutoMigrate`, `internal/database/db/db.go:88`)

```
User    { ID, Username (unique), PasswordHash (bcrypt), Role ("admin"|"user"),
          AnilistAccountID *uint, CreatedAt }
Session { ID, Token (unique, indexed), UserID, ExpiresAt, CreatedAt }
```

### 4.2 Add `user_id` to per-user rows

`Account` (link to a User), continuity store, `Playlist`, `AutoSelectProfile`,
`AutoDownloaderRule` (if per-user), `Theme`, silenced/notification state.
`LocalFiles`, `TorrentstreamHistory`, manga mappings, metadata, custom source
stay shared (universal AniList media IDs).

### 4.3 Split `models.Settings`

`models.Settings` (`models.go:47`) is embedded sub-structs. Cut along admin/user:

- **ServerSettings (admin, one row):** Library paths/scanner/providers, Torrent,
  Debrid, Torrentstream, Mediastream, MediaPlayer host paths (ffmpeg/ffprobe),
  online-stream enable, extension secure mode, DoH, update channel, Nakama host.
- **UserSettings (per user, `user_id`):** Theme (whole table), Discord (§6),
  Notifications, Anilist view prefs, home items / pinned menu / default sorting,
  `autoUpdateProgress`, `autoPlayNextEpisode`, `enableWatchContinuity`,
  `defaultPlaybackSource`. Local download dir / external player / desktop player
  selection are **client-side** (§6) — store per-user but the server never acts
  on them.

Several `LibrarySettings` fields are per-user (progress/continuity/playback)
while most are server — do this field-by-field, don't move the struct wholesale.

### 4.4 "Own library" = shared files ∩ own AniList

Auto-scan runs as admin and produces shared `LocalFiles` keyed by universal
AniList media IDs. A user's library *view* = shared local files joined to *their*
AniList collection/progress. No per-user file scanning in v1.

---

## 5. Auth & identity flow

Keep the server password as the **outer network gate**; add a user identity layer
behind it.

```
client → [server password gate]            (existing X-Seanime-Token / ws token)
       → POST /api/v1/auth/login {username, password}
            → verify bcrypt, issue Session.Token
       → every request: Authorization: Bearer <session token>
            → middleware resolves token → UserID → c.Set("userId", id)
       → WS connect: ?session=<token>
            → handler sets WSConn.UserID
```

- **Middleware** (`server_auth_middleware.go`): after the password check, resolve
  the session token to a `UserID` and stash it in echo context. Handlers read it
  via a helper `h.UserID(c)` and call `app.SessionFor(id)`.
- **Server-side sessions, not JWT** — simpler revoke, fits the single process;
  JWT buys nothing here (open decision in `profile-support.md`, leaning sessions).
- **First-run bootstrap:** first `User` created = `admin`. Migrate the existing
  single `Account`/`Settings` to that admin. **Recovery:** a CLI flag /
  `--reset-admin` so a lost admin password doesn't brick the server.
- **AniList linking is separate:** Seanime login ≠ AniList. After login a user
  links their AniList (the existing OAuth → `LoginToAnilist` path, rebound to
  write `Account{user_id}` instead of `{ID:1}`).

---

## 6. Event scoping

1. `WSConn` gains `UserID` (set from the session token at upgrade,
   `websocket.go:60-67`).
2. Add `SendEventToUser(userID, t, payload)` to `WSEventManager` — same loop as
   `SendEvent` but filtered by `conn.UserID` (a user may have several tabs).
3. Per-session modules (§2) capture their `userID` and call `SendEventToUser`.
   This converts the bulk of the 258 broadcast sites by construction.
4. Genuinely global events (server announcements, `ServerReady`, admin settings
   changed) stay `SendEvent`.

Client-side concerns that the **server cannot drive** — Discord Rich Presence
(local Discord IPC, `internal/discordrpc`), local media players (VLC/MPV launch
on the user's box), the local download directory — move into the **Denshi
desktop client**, fed by that user's scoped playback events. Store the prefs
per-user, but the server treats them as opaque.

---

## 7. Watch party (Nakama) — redesign for same-backend

Current Nakama is peer↔host federation between *separate* Seanime instances
(`internal/nakama/`, host shares library, peers stream from host). For users on
**one** backend that's the wrong transport.

- Build a lightweight **session room** on the existing WS bus: one user opens a
  room, any logged-in user joins, the server relays play/pause/seek/buffer.
- **Reuse the reconciliation math** in `watch_party_syncing.go` (and its test
  `watch_party_syncing_test.go`) — that logic is transport-agnostic and already
  tested. Drop the host/peer connection, room-relay, and library-sharing
  scaffolding (`connect.go`, `host.go`, `peer.go`, `share.go`).
- All participants share one backend/debrid, so they resolve the **same** stream
  URL — no per-user re-derivation.

Keep the cross-instance Nakama as-is for the existing federation use case; the
new same-backend rooms are a parallel, simpler path.

---

## 8. Resource concurrency (ship with the feature, not after)

One debrid account, one torrent engine, one transcoder, shared by N users:

- Debrid: respect the provider's concurrent-stream cap; queue/limit per provider.
- Torrentstream: bound simultaneous streamed torrents; per-user fairness.
- Mediastream/ffmpeg: a semaphore on concurrent transcodes sized to the VPS.

`ponytail:` start with a single global semaphore per resource; add per-user
fairness only if one user can starve others in practice.

---

## 9. Frontend

- **Login UX:** behind the existing password gate, add a username/password login
  that calls `/api/v1/auth/login` and stores the session token (extend
  `serverAuthTokenAtom` / requests layer to send `Authorization: Bearer`).
- **Role gating:** add `role` to `serverStatus.user`. In `settings/page.tsx`,
  render the admin tabs (Video Playback, Torrent Provider/Client/Streaming,
  Debrid, Online Streaming, Transcoding, Server) **only when `role === "admin"`**.
  Regular users see App / UI / Nakama / Discord / Logs-lite. This is conditional
  rendering off one field — the screenshot's sidebar is already per-section.
- **Status blob per user:** `/api/v1/status` returns the *acting user's*
  settings/account; admin-only fields omitted/locked for regular users.

---

## 10. Phased plan (each phase independently verifiable)

1. **Identity core.** `User` + `Session` tables; bcrypt; `/auth/login`;
   middleware resolves session→userID; first-run admin bootstrap migrating the
   existing account/settings; `--reset-admin`. *Verify:* two users log in, each
   gets a distinct session; non-admin blocked from admin endpoints.
2. **Settings split.** `ServerSettings` (admin) vs `UserSettings` (per-user) +
   migration. Role-gated settings UI. *Verify:* admin edits debrid; regular user
   cannot see/POST it; user edits theme independently.
3. **`UserSession` + per-user platform.** Session bundle holding the user's
   platform + collection; `app.SessionFor`. Rebind `LoginToAnilist` to write
   `Account{user_id}`. *Verify:* two users link different AniList accounts, each
   sees their own collection.
4. **Per-user stateful modules + streaming split (§3A).** Move PlaybackManager /
   DirectStream / VideoCore / NativePlayer / LibraryExplorer into the session,
   shared engines injected. Split the torrentstream/debrid **session** layer
   (currentTorrent / selection / tracking / `previousStreamOptions`) out of the
   shared engine into the session. Key the serve URLs by stream token (§3A.3).
   *Verify:* clin and josh stream different anime **simultaneously**, each with
   independent selection/progress; clin starting on a 2nd device transfers within
   clin's clients only and never disturbs josh.
5. **Event scoping.** `WSConn.UserID` + `SendEventToUser`; per-session sends.
   *Verify:* user A's scan/playback events never reach user B.
6. **Per-user data CRUD.** `user_id` on continuity, playlists, auto-select,
   theme. *Verify:* resume positions and playlists are isolated.
7. **Watch-party rooms.** Same-backend rooms reusing the sync math. *Verify:*
   two users join a room; play/pause/seek stays in sync.
8. **Concurrency limits.** Semaphores on debrid/torrentstream/transcode.
9. **Client-side relocation.** RPC / local player / downloads as Denshi per-user
   features fed by scoped events.

Phases 1–2 ship value alone (admin-locked server, multiple logins) before the
heavy session refactor in 3–5.

---

## 11. Risks & open decisions

- **Biggest risk:** phases 4–5 (per-user stateful modules + event scoping). The
  modules cache collection and emit globally today (§1.2/1.3); estimate effort
  there, not on the `User` table.
- **Do NOT** multi-tenant the content engines (debrid/torrentstream/transcode/
  scanner). Per-user copies = the trap that turns this into a rewrite.
- **LocalManager / offline** is single-user; per-user offline snapshots are a
  later, separate effort — keep offline mode single-user for v1.
- **Plugin context** (`plugin.GlobalAppContext`) is global and wired with the
  single platform/managers (`app.go`, `modules.go`). Decide: plugins run as
  admin/shared in v1 (simplest) vs per-user — lean shared.
- **Auto-downloader rules:** admin-global (owns the library) vs per-user — lean
  admin v1.
- **Session token transport:** `Authorization: Bearer` vs reuse the
  `X-Seanime-Token` slot — lean Bearer to keep the password gate separate.
- **Backward compat (binding):** with only the bootstrapped admin and no extra
  users, behavior must match today byte-for-byte.

---

## 12. Testing

- Reuse `watch_party_syncing_test.go` for the room sync math (unchanged logic).
- New: middleware session-resolution test (valid/expired/forged token → user).
- New: isolation test — two `UserSession`s, assert collection/progress/events do
  not cross (the core multi-tenant guarantee).
- New: migration test — existing single-user DB → admin user owns prior
  account/settings; single-user behavior unchanged.
