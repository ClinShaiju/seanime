# Profile / multi-user support — design writeup

Status: investigation / proposal. No code written yet.

## Goal

Turn the VPS Seanime backend into a **shared server that multiple people log
into**, each with their own profile:

- **Admin** (the server owner) controls all *server/infrastructure* settings —
  debrid, torrent provider/client, torrent streaming, transcoding/direct-play,
  online streaming providers, library path, extensions.
- A **regular user** only controls *their own* stuff — app/UI customization,
  theme, Discord rich presence, local download directory, external player,
  notifications.
- **First access** for a regular user: enter the server password, then their
  own username + password. That loads *their* settings, *their* collection/
  progress, *their* AniList link.
- **Watch party (Nakama):** any user connected to the server can create a watch
  party; any other connected user can join.

This is the missing piece that makes the existing "Denshi points its renderer at
the VPS" setup actually usable by more than one person (see
`[[denshi-external-server]]`). Today every Denshi client that points at the VPS
shares the *same* single account, library, and settings — there is no per-user
isolation at all.

## The core reality — Seanime is single-tenant to the bone

This is the binding constraint and the reason this is a large effort, not a
toggle. Every layer assumes exactly one user:

| Layer | Evidence | Implication |
|---|---|---|
| **Settings** | `models.Settings` is one embedded row at `id = 1`; cached in a package-global `db.CurrSettings` (`internal/database/db/settings.go`). | One library path, one debrid key, one transcode config for the whole process. |
| **Account** | `db.GetAccount()` does `gormdb.Last(&acc)` into a package-global `accountCache` (`internal/database/db/account.go`). One AniList token, one viewer. | The whole app tracks to a single AniList account. |
| **Auth** | One `server.password` → one `ServerPasswordHash`. The `X-Seanime-Token` header *is* that single hash (`internal/handlers/server_auth_middleware.go`). | The password gates *the server*, it does not identify *a user*. There is no notion of "who" is making a request. |
| **Runtime singletons** | `platform.Platform` (AniList), `playbackmanager`, `continuity`, library scanner, extension repo, `mediastream`, `torrentstream`, `debrid/client` — all constructed once in `core.App` and shared. | "Current user" is implicit and global. |
| **WS event bus** | `wsEventManager.SendEvent(...)` broadcasts to connected clients; events aren't scoped to a user. | Scan progress, playback state, notifications all leak across clients. |

### Nakama is NOT multi-user-on-one-server

It's easy to assume Nakama already does this. It does not. Nakama is **peer↔host
federation between separate, full Seanime instances**
(`internal/nakama/`): a *host* shares its own anime library, *peers* are other
people each running their *own* complete Seanime backend (own account, own
settings, own library) who connect in and stream *from the host*. The auth is a
separate `HostPassword`, and a peer connects via `RemoteServerURL` +
`RemoteServerPassword`.

So Nakama's shape is "many backends, one shared library." The profile vision is
the opposite: **one backend, many users.** The watch-party *sync* logic
(play/pause/seek/buffer reconciliation in `watch_party_syncing.go`) is reusable;
the federation/library-sharing scaffolding around it is the wrong shape for
same-backend users (more on this below).

## What "own library" actually means here

Worth nailing down before designing, because it changes the scope by an order of
magnitude. The content source on a shared VPS is **shared infrastructure**: one
debrid account, one torrent-stream client, one transcoder, one library folder
(admin-owned). Users are not each uploading their own files.

So a user's "own library" is **their own view and tracking state over shared
content**, not their own files:

- Own AniList account → own collection, own lists, own scores.
- Own progress / resume positions (`continuity`).
- Own continue-watching, own playlists, own auto-downloader rules / auto-select
  profiles.
- Own UI: theme, home layout, pinned menu items.

This gives the key architectural seam:

> **Two planes.** A *content/infrastructure plane* (debrid, torrentstream,
> transcode, library scan, extensions, online-stream providers) that stays a
> single shared singleton owned by admin — **do not multi-tenant it.** And an
> *identity/state plane* (account, progress, settings-lite, theme, playlists,
> rules) that gets partitioned per user.

Multi-tenanting only the identity/state plane is what keeps this tractable. You
do **not** need a debrid client per user or a transcoder per user — those are
genuinely shared. You need per-user *identity, request context, data
partitioning, and event scoping*.

## Settings split — the hard boundary

The single `Settings` blob must be cut into two tables along the
admin/user line. This split is the contract the whole feature rests on.

**Server settings (admin only, shared, one row):**

- `LibrarySettings` (library path, scanner config, providers) — mostly server.
- `TorrentSettings`, `DebridSettings`, `TorrentstreamSettings`,
  `MediastreamSettings` — server infrastructure.
- `MediaPlayerSettings` *host-side bits* (ffmpeg/ffprobe paths).
- Online streaming enable, extension secure mode, DoH, update channel.

**User settings (per user):**

- `Theme` (entire table is already UI-only → becomes per-user).
- `DiscordSettings` (rich presence) — **but client-side, see inconsistencies**.
- `NotificationSettings`.
- `AnilistSettings` (hide audience score, adult content, blur) — view prefs.
- App customization (home items, pinned menu items, default sorting).
- `library.autoUpdateProgress`, `autoPlayNextEpisode`,
  `enableWatchContinuity`, `defaultPlaybackSource` — per-user playback prefs.
- Local download directory, external player link, desktop media player
  selection — **client-side, see inconsistencies**.

Some fields in `LibrarySettings` are genuinely per-user (progress, continuity,
playback source) while most are server. The clean move is to **stop embedding
one `Settings` struct** and instead have `ServerSettings` (admin) +
`UserSettings` (FK `user_id`), pulling the per-user fields out of the existing
sub-structs.

## Inconsistencies & challenges (the things that bite)

These are the items that make the naive version wrong:

1. **Discord Rich Presence is inherently client-side.** It writes to the local
   Discord IPC socket (`internal/discordrpc`). A VPS backend physically cannot
   set rich presence on a user's desktop Discord. It's in the "regular user"
   list, but it can only work if it runs in the **Denshi desktop client** per
   user, driven by that user's playback events — not as server-side config that
   does anything. Web-only users get no RPC.

2. **Local download dir, external player, desktop media player (VLC/MPV/MPC) are
   client-side too.** The VPS can't launch VLC on someone's laptop or write to
   their Downloads folder. These "per-user settings" are really **per-client**
   settings that only mean anything inside that user's Denshi/desktop instance.
   Store them per-user if you like, but the server can't act on them — the
   client does.

3. **Two-layer identity.** "username + password" is a *Seanime-local* credential
   (new `User` table, bcrypt hash, role). It is **not** the AniList login.
   Each Seanime user then **links** their AniList account (OAuth) separately.
   So: server password (gate) → Seanime user login (identity) → AniList link
   (tracking). Three distinct secrets, today there is only the first.

4. **Request context threading — the biggest cost.** Almost nothing in the
   backend takes a "user". To resolve a request to a user you must thread a
   user id from the auth middleware through every handler into the modules that
   currently read `db.GetAccount()` / `db.CurrSettings` globals. Either (a)
   request-scoped context carrying the user + their platform/continuity/settings,
   or (b) a per-user "session" object cached server-side keyed by user id. Both
   mean unwinding the package-global singletons for the identity/state plane.

5. **WebSocket event scoping.** `SendEvent` currently fans out to all clients.
   Multi-user requires every event to carry/select a target user so user A
   doesn't see user B's scan progress, playback sync, or notifications. The WS
   layer needs a client→user registry and per-user send. This touches a lot of
   call sites.

6. **Shared debrid = shared limits.** One debrid account has concurrent-stream
   caps and rate limits. N users streaming simultaneously will contend. Same for
   the single torrent-stream client (one engine, shared bandwidth/disk) and the
   transcoder (ffmpeg is CPU-heavy; N simultaneous transcodes can melt a VPS).
   Needs **concurrency limits / a queue** per resource, and probably per-user
   fairness. This is an operational reality, not optional polish.

7. **AniList tracking writes.** With per-user tokens, progress updates must write
   to the *acting user's* AniList, via that user's `platform.Platform`. The
   single global platform instance has to become per-user (cheap object, but
   another singleton to unwind).

8. **Watch party shape mismatch.** Current Nakama watch party assumes each
   participant is a separate instance streaming from the host. For same-backend
   users, all participants already share one backend, one content source. The
   right model is a lightweight **session room on the existing WS bus**: one user
   starts a session, others join, the server relays play/pause/seek/buffer state
   (reuse the reconciliation math from `watch_party_syncing.go`) and everyone
   streams the same shared source URL. Dragging in Nakama's host/peer/relay
   federation here is over-engineering — the federation exists to bridge
   *separate* servers, which no longer applies. Reuse the sync algorithm, not the
   transport.

9. **Offline/local sync (`internal/local`)** is built to snapshot one account's
   collection + local files for offline use. It doesn't fit multi-tenant as-is;
   per-user offline snapshots are a later, separate problem.

10. **Extensions are global.** Loaded once into a shared repo. Keep them
    admin-managed and shared (don't multi-tenant). Per-user extension enablement
    is a possible later refinement, not v1.

11. **First-run bootstrap & lockout.** First account created becomes admin.
    Need a recovery path (admin password reset via config flag / CLI) so a
    forgotten admin password doesn't brick the server.

## Necessary features for a seamless experience

Pulling the above into a build list:

- **`User` table**: id, username, bcrypt password hash, role (`admin`/`user`),
  created_at. First user = admin.
- **Session auth**: issue a per-user session token / JWT on login (carries user
  id + role). Auth middleware resolves *every* request to a user, replacing the
  "is this the single password hash?" check. Keep the server password as the
  outer gate (or fold it into the login page).
- **Settings split**: `ServerSettings` (admin, shared) + `UserSettings` (FK
  `user_id`). Admin endpoints write server settings; user endpoints write only
  their own.
- **Per-user data partitioning**: add `user_id` to `Account`, continuity store,
  playlists, auto-downloader rules, auto-select profiles, theme,
  silenced/notification state. Shared/content tables (torrentstream history,
  manga mappings, metadata, custom source) can stay shared or get a user_id only
  where progress-like.
- **Per-user runtime context**: per-user `platform.Platform` (AniList) +
  continuity view, resolved from the request's user. Content-plane modules
  (debrid, torrentstream, transcode, scanner) stay shared singletons.
- **WS event scoping**: client→user registry; per-user `SendEvent`.
- **Admin-gated settings UI**: the settings sidebar in the screenshot
  (Video Playback, Torrent Provider/Client/Streaming, Debrid, Online Streaming,
  Transcoding) renders **only for admin**; regular users see App / UI /
  Nakama / Discord / Logs-lite. Drive this off the role in the session.
- **AniList link flow per user**: each user links their own AniList from their
  profile page.
- **Watch-party session rooms**: server-side room keyed by host user; join by
  any connected user; relay sync over WS; reuse `watch_party_syncing` math.
- **Resource concurrency controls**: per-resource limits/queue for transcode,
  debrid concurrent streams, torrent-stream sessions; per-user fairness.
- **Client-side relocation** of rich presence / local player / local downloads:
  acknowledge these live in the Denshi client, fed by per-user playback events.
- **Admin recovery**: CLI/flag to reset admin credentials.

## Two ways to build it

### Option A — true multi-tenant single backend (the literal ask)

One process, the identity/state plane partitioned per user, the content plane
shared and admin-owned. This is what the UX above describes. It's the right
model and it's a large rearchitecture, dominated by items #4 and #5
(request-context threading + WS event scoping) rather than the schema work.

### Option B — per-user backend instances behind a gateway (the lazy path)

Run one Seanime process per user on the VPS (separate data dirs), put a thin
auth/proxy in front that routes a login to that user's instance, and push the
admin-locked server settings into each instance's config. **Watch party works
for free** — it's literally what Nakama already does between instances.

Trade-off: it sidesteps almost all the code change (the single-tenant binary is
untouched) but it does **not** give a shared content plane — each instance needs
its own debrid/torrent/transcode config (admin can template it, but it's N
debrid sessions, N transcoders, N processes' worth of RAM). It scales badly past
a handful of users and burns resources. Good for 2–5 trusted users; wrong for
"a server anyone logs into."

**Recommendation:** the user's described UX (one login loads your state, admin
owns shared debrid/transcode, everyone is "on the server") is Option A. Build A,
but scope it by the two-plane seam: **only multi-tenant the identity/state
plane; leave the content/infra plane as the existing shared singleton.** That is
the difference between "rewrite Seanime" and "add a user dimension to the parts
that track a person." If the real near-term need is 2–5 people, Option B ships
this week with zero backend changes and watch party already working — worth
saying out loud before committing to A.

## Phased plan (Option A)

1. **Identity**: `User` table + bcrypt + roles + session auth middleware that
   resolves every request to a user. First-run admin bootstrap + recovery flag.
   Login UI (server password → user login).
2. **Settings split**: `ServerSettings` (admin) vs `UserSettings` (per-user);
   migrate existing single row → server settings + one admin user's settings.
   Admin-gated settings UI off the session role.
3. **Per-user account + AniList**: `user_id` on `Account`; per-request
   `platform.Platform`; per-user AniList link flow.
4. **Per-user state**: `user_id` on continuity, playlists, auto-downloader,
   auto-select, theme; per-user reads/writes.
5. **WS event scoping**: client→user registry; per-user event delivery.
6. **Watch-party session rooms**: same-backend rooms reusing the sync math;
   create/join by any connected user.
7. **Resource limits**: concurrency/queue for transcode, debrid, torrentstream.
8. **Client-side**: rich presence / local player / downloads as per-user Denshi
   client features fed by scoped events.

## Open decisions

- **Server password vs. user login** — keep both (password = outer gate, login
  = identity) or collapse into a single login page once users exist? Leaning:
  keep server password as the network boundary, add login on top.
- **JWT vs server-side sessions** — sessions are simpler to revoke and fit the
  existing single-process model; JWT buys nothing here. Leaning: server-side
  sessions keyed by token.
- **How much of `LibrarySettings` is per-user** vs server — needs a field-by-
  field pass (progress/continuity/playback = user; paths/scanner/providers =
  server).
- **Shared library scan visibility** — do all users see the same local library
  collection (shared content, per-user progress), or can users have private
  sub-libraries? Shared-with-per-user-progress is the lazy correct v1.
- **Watch party content source** — confirm all participants resolve the same
  shared stream URL (they share one backend/debrid, so yes) rather than
  re-deriving per user.
- **Per-user vs shared extensions / online-stream providers** — shared for v1.

## Risks / notes

- The dominant cost is **unwinding identity-plane singletons + WS event
  scoping** (#4, #5), not the database schema. Estimate effort there, not on the
  `User` table.
- Do **not** multi-tenant the content plane (debrid/torrentstream/transcode/
  scanner). It's shared by design; per-user copies are the trap that turns this
  into a rewrite.
- Rich presence / local player / local downloads cannot be server-driven —
  resist putting them in server settings as if they do something.
- Shared debrid/transcode **will** hit concurrency limits with real multi-user
  load; ship the limits with the feature, not after.
- Keep single-user installs byte-for-byte unchanged: if only the admin exists
  and no extra users are created, behavior must match today.
