# Fork Audit — `ClinShaiju/seanime` vs upstream `5rahim/seanime`

**Scope:** all 105 commits since the fork point `315c8c9e` (`git merge-base main origin/main`).
**Diff size:** 188 files, ~18.4k insertions / ~2.8k deletions.
**Method:** per-feature deep read of the *current* code plus the commit evolution, looking for
correctness bugs, inconsistencies, performance issues, and improvement opportunities. Findings are
severity-tagged and carry `file:line` refs. Empirically-confirmed items say so.

Severity legend: **[H]** high (wrong results / crash / data leak), **[M]** medium (wrong results in
a common case, recoverable), **[L]** low (cosmetic / brittle / dead code), **[+]** positive note.

---

## Executive summary — prioritized

Overall the fork is well-engineered: heavily commented, mostly tested, and the security-sensitive
multi-user layer is genuinely careful (bcrypt, crypto/rand sessions, server-side role gating, per-user
cache isolation). The issues worth acting on, in order:

| # | Sev | Area | Issue | Where |
|---|-----|------|-------|-------|
| 1 | **H** | Nakama rooms | Live `*WatchRoom` JSON-marshaled outside `room.mu` while membership mutates its map → fatal *concurrent map read/write* crash | `watch_room.go:793`, `nakama_rooms.go:91,118` |
| 2 | **M** | Season grouping | Merged-season progress reads the **admin** collection, not the user's → wrong/leaked progress for every non-admin (CONFIRMED outlier) | `anime_franchise.go:235` |
| 3 | **M** | Autoselect | `[Multiple Subtitle]`/`Multi-Subs` (JP-audio) misread as dual-audio → English-dub tier (CONFIRMED via probe) | `comparison.go:310,344` |
| 4 | **M** | Debrid stream | Unsynchronized `StreamManager` scalars read by Nakama/watch-room goroutines while the start goroutine writes them → data race | `stream.go` / `repository.go:367,402` |
| 5 | **M** | Debrid stream | `Repository.previousStreamOptions` is one global slot; last streamer clobbers it for all users | `stream.go:248` |
| 6 | L/M | Season grouping | A transient AniList failure caches an under-populated franchise group for 24h | `anime_franchise.go:107` |
| 7 | L/M | Nakama rooms | Zombie rooms never reaped when all clients drop without an explicit leave | `watch_room.go` (`HandleClientDisconnect`) |
| 8 | L/M | Debrid prewarm | Multi-user prewarm can approach TorBox's 60/hr `createtorrent` cap (already forced disabling metadata prewarm) | `prewarm.go` |
| 9 | L | Profile | `SendEventToUserOrUnscoped` leaks per-user stream events (incl. torrent names) to pre-login `UserID==0` clients | `websocket.go:215` |
| 10 | L | Profile | `UserOverrides.DebridApiKey` carries a serializing json tag (safe today, latent leak) | `user_settings.go:39` |
| — | L | Misc | Dead scoring constants; two-path ranking inconsistency; missing panic guard/bounds in `findBestTorrentFromManualSelection`; TorBox slice-append race; `NAKAMA_ROOM_DEBUG` left enabled; font cross-routing; Go/TS stem-regex divergence | see sections |

Latency/prewarm optimization opportunities (not bugs) are in **§7**, and the Tenji client + AIOStreams
fork are audited in **§8** and **§9**.

**Do first:** #1 (crash), #2 and #3 (wrong user-visible results), then #4/#5 (concurrency). #1, #4, #5
all stem from the same root cause — sharing mutable state across goroutines without a lock or a snapshot —
and #1 + #7 + the leftover debug logging all live in the still-stabilizing rooms feature, so a single
"harden watch rooms" pass (snapshot serialization + room reaper + strip diagnostics) closes most of the
high-value backlog.

---

## Implementation status — 2026-06-24 (build + vet clean; autoselect, nakama `-race`, events tests pass)

Summary-table row #1 (Nakama crash) and #7 (zombie rooms) were already fixed in a prior pass (see §5
status block). This pass implemented the remaining actionable in-repo findings:

| # | Sev | Status | What changed |
|---|-----|--------|--------------|
| 2 | M | **FIXED** | `anime_franchise.go` merged-season now reads `sess.GetAnimeCollection(false)` (per-user), not `h.App.GetAnimeCollection`. |
| 3 | M | **FIXED** | `comparison.go` name-level dual-audio match now requires an audio token via `dualAudioNameRe = (dual\|multi)[\s._-]?audio`; bare `multi`/`dub` no longer flips `[Multiple Subtitle]` into the dub tier. `parsed.AudioTerm` matching unchanged. |
| 4 | M | **FIXED** | `StreamManager` share scalars (`currentStreamUrl`/`currentTorrentItemId`/`currentFileId`/per-user `previousStreamOptions`) now guarded by `stateMu` via get/set helpers; `GetUserStreamShare` reads a consistent triple via `shareSnapshot()`. **Note:** the two `context.CancelFunc` fields are deliberately left unlocked (idempotent → benign double-cancel/ctx-leak) — marked with a `ponytail:` comment naming the upgrade path. |
| 5 | M | **FIXED** | `Repository.previousStreamOptions` now guarded by `prevOptsMu` (setter/getter); documented as the global last-active (host/plugin) slot. Also fixed the §2-[L] inconsistency: `playPreloadedStream` now sets the per-user slot too. |
| 6 | L/M | **FIXED** | `resolveFranchiseGroup` captures `FetchMediaTree`'s error and only `CacheFranchiseGroup`s when it succeeded, so a transient AniList failure no longer poisons the 24h group cache. |
| 9 | L | **FIXED** | `websocket.go` `SendEventToUserOrUnscoped` skips anonymous `UserID==0` clients when `requireUserScoping` is set (wired from `cfg.Server.Password != ""` in `app.go`), closing the per-user stream-event leak on networked servers; local/desktop installs (no password) keep the unscoped fan-out. |
| 10 | L | **SKIPPED** | `UserOverrides.DebridApiKey` `json:"-"` would break persistence — `UserOverrides` is round-tripped as a JSON blob (`db/user_settings.go` `json.Marshal`/`Unmarshal`), so the tag drop would stop the per-user debrid key from being stored. Audit's suggested fix conflicts with the persistence mechanism; left as-is (already rated safe-today). |
| — | L | **FIXED** | Dead scoring constants `scoreLanguageBase`/`scoreLanguageDecay`/`scoreLanguageUnpreferred` + misleading comment deleted (`comparison.go`). |
| — | L | **FIXED** | `findBestTorrentFromManualSelection` got the missing `defer util.HandlePanicInModuleWithError(...)` guard + a bounds check on `info.Files[fileIndex]` (UI-supplied index). |
| — | L | **FIXED** | TorBox `getTorrentsCached` now returns `slices.Clone` copies in both paths, so a lock-free dedup iterator can't race `AddTorrent`'s in-place append. |

### §8 Tenji (`H:/Projects/seanime-tenji`) + §9 AIOStreams (`H:/Projects/AIOStreams`) — implemented 2026-06-24

| Ref | Sev | Status | What changed |
|-----|-----|--------|--------------|
| §8 | M | **FIXED** | Tenji read the season-grouping toggle from the legacy `serverStatus.settings.library.groupSeasons`; aligned all 6 read sites (`group-seasons.ts` ×5, `season-switcher.tsx` ×1) to the canonical `serverStatus.themeSettings.groupSeasons`, matching seanime-web. Tenji has no write path (it consumes the toggle the web Theme tab sets), so a read-side alignment is sufficient. The hand-synced generated `Models_Theme` was stale (missing `groupSeasons`/`hideFranchiseSpinoffs`/`hideFranchiseRecaps`) — added the three fields to match the server so it typechecks. `tsc --noEmit` clean for the changed files. |
| §8 | L | **FIXED** | Tenji's `franchiseTitleKey` roman-numeral strip changed from `\s(ii…vii)\s` to `\s(ii…vii)\b` to match Go's `FranchiseTitleStem`. (seanime-web carries the identical `\s…\s` regex at `_features/anime-library/_lib/group-seasons.ts:38` — same one-line `\b` change would bring all three into parity; left untouched as it's in the seanime repo and outside this request.) |
| §8 | L | **DEFERRED** | Tenji's inherited `NAKAMA_ROOM_DEBUG` round-trip left in place, consistent with §5 — still load-bearing for verifying the not-yet-deployed watch-room playback fix. Strip alongside the server/web one. |
| §9 [6] | — | **FIXED** | AIOStreams Seanime torrent-provider extension: added a module-level cache (`animeEntryCache`) for the serial `aiostreams.anime(id.type, id.value)` resolution that precedes the parallel search fan-out (`main.ts`). The anilist/kitsu→id mapping is stable per media, so caching it for the runtime's lifetime removes one RTT from every repeat search (prewarm-then-play, next-episode). Only non-null results are cached so a transient miss is retried. (Deps unavailable in this env → not tsc-run; type-safe by construction — the alias erases at build and the value type stays `AIOStreamsAnimeEntry \| null`, the contract `buildSearchId` already accepted.) |
| §9 [1]–[5] | — | **N/A (config)** | Per the audit these are runtime AIOStreams `userData`/env config (dynamic addon fetching + exit condition, `BUILTIN_DEFAULT_ANIMETOSHO_TIMEOUT`, addon `mediaTypes`, `streamsCache` TTL, `precacheNextEpisode`) — no fork-code change to implement; apply them on the deployed instance. |

**Out of scope / not done here:** §7 latency/prewarm levers (optimizations, not bugs); the two-path ranking inconsistency and `scoreBand` fragility (§1, latent,
no current user impact); font cross-routing (§6, needs a per-stream token); Go/TS stem-regex divergence
(§4/§8, client-side); `NAKAMA_ROOM_DEBUG` (still load-bearing — see §5); `UserSession.Events()` per-call
alloc (§3, harmless).

**Adjacent issue found while verifying (pre-existing, unrelated to the audit):**
`TestDownlaoded_KeepItemOnDownloadUrlFailure` (`repository_test.go:196`) builds `Repository` with a bare
struct literal that omits `queuedDownloadFailures`, so `processQueuedDownloads` panics on a nil
`*result.Map` at `download.go:104`. Fails identically on the pre-change tree (confirmed via stash). One-line
fix (init the map in the literal) but left untouched as out-of-audit-scope.

---

## 1. Autoselect / torrent ranking
Files: `internal/torrents/autoselect/{comparison,autoselect,file_selection}.go`,
`internal/util/release_name.go`, `internal/torrents/torrent/search.go`,
`internal/extension/hibike/torrent/types.go`, `internal/torrents/analyzer/analyzer.go`,
`internal/debrid/client/finder.go`.
Commits: a00983fe, 37d5a2ab, f10af77d, 3afdb6c8, ca76dd22, aa346c98, 166e2960, 7ed57855, 5063ef77,
33744c20, a0f7a965, 30df6a48, 4fd80519, 70f34f49, bc317d74, 8b3c1b1b, 38dc857f, 69991b5d, 81832704 …

### Findings

**[M] Multi-*subtitle* releases are misclassified as dual-*audio* → English-dub tier. (CONFIRMED)**
`comparison.go:310` — `isDual := containsMultiOrDual(parsed.AudioTerm) || containsMultiOrDual([]string{c.lowerName})`.
`containsMultiOrDual` (`comparison.go:344`) matches the bare substring `"multi"`, so a name containing
`[Multiple Subtitle]` / `Multi-Subs` (ubiquitous for Erai-raws, SubsPlease batches, etc.) flips a
Japanese-audio release into the `+2000` English-dub band via `audioLanguageScore`.
Verified with a probe: name `[Erai-raws] Show - 01 [1080p][Multiple Subtitle].mkv` →
`audioTerm=[] subtitles=[Multiple Subtitle] language=[]` yet `audioLanguageScore=2000 (=scoreEnglishDub)`.
Impact: for a dub-preferring profile, JP-audio multi-sub releases land in the top tier and can be
auto-selected over a *real* dub on the size/seeders tiebreak. Existing tests miss it because none pair
a multi-sub name with a non-empty `PreferredLanguages`.
Fix: at the name level require an audio-specific token (`"dual audio"`, `"dual-audio"`, `"multi audio"`,
`"multi-audio"`, `"dualaudio"`), not bare `"multi"`/`"dub"`. Keep `parsed.AudioTerm` matching as-is.

**[L] Dead constants + a misleading comment.** `comparison.go:26-31` define `scoreLanguageBase`,
`scoreLanguageDecay`, and `scoreLanguageUnpreferred` — none are referenced anywhere (grep-confirmed).
They are leftovers from the pre-`audioLanguageScore` incremental language scoring. The 4-line doc comment
on `scoreLanguageUnpreferred` describes a Russian-only-penalty behavior that no longer exists. Delete all three.

**[L] Two ranking paths can disagree within a band.** `sortCandidates` (`comparison.go:565`) orders by
`(priority, bonus, score)`; `smartCachedPrioritization` (`comparison.go:597`) orders by
`(scoreBand(score), cached, score)`. Within one band the keys differ (priority-then-bonus vs total
score), so when `bonus` tips the total, the torrent-client auto-download order and the debrid
Rank/auto-select order can differ for the same inputs. Both debrid paths agree with each other, so user
impact is small — but it's a latent inconsistency.

**[L] `scoreBand` boundary is coincidental for the ambiguous-batch case.** An unlabeled S1 batch
(`-scoreSeasonAmbiguousBatch = -3000`) plus an English dub (`+2000`) sums to `-1000` before the format
bonus, landing exactly on the band-1/band-2 threshold (`scoreBand`: `score <= -1000`). A format bonus
(e.g. `+scoreRemux = 100`) silently moves it across the band boundary. It passes today's tests but the
magnitudes are tightly coupled and fragile to future constant changes.

**[L] `findBestTorrentFromManualSelection` lacks the panic guard + a bounds check.**
`finder.go:218` has no `defer util.HandlePanicInModuleWithError(...)` although its sibling
`findBestTorrent` (`finder.go:58`) does; and `info.Files[fileIndex]` (`finder.go:325`) is not
range-checked against a UI-supplied `chosenFileIndex`. A bad index panics the handler goroutine.

**[+] Solid work in this vertical.** `CleanReleaseName`/size-token stripping (`release_name.go`),
flag-emoji language decoding, `AnimeTorrent.Identity()` dedup, episodic-format→TV provider normalization
(`search.go`), multi-cour tree re-resolution (`finder.go:297`), and the shared media container
(`analyzer.go` `PrepareSharedContext`, avoids N AniList tree fetches) are all correct, well-commented,
and mostly tested. The autoselect suite passes.

---

## 2. Debrid streaming / preload / prewarm / reconnect
Files: `internal/debrid/client/{stream,repository,finder}.go`, `internal/core/prewarm.go`,
`internal/debrid/torbox/torbox.go`, `internal/directstream/{serve,httpstream}.go`.
Commits: 37d5a2ab, c38ae2bd, 54ed3a30, 00acd125, 9b969cc3, c5a22f4e, 1719bcd8, af16ba17, cbeb92c6,
17f118ea, 643f6cce.

### Findings

**[M] Data race on `StreamManager`'s scalar fields. (CONFIRMED by code paths)**
Only `preloadMu` guards the preload maps. The scalar fields `currentStreamUrl`, `currentTorrentItemId`,
`currentFileId`, `downloadCtxCancelFunc`, `playbackSubscriberCtxCancelFunc`, and `previousStreamOptions`
are written by `startStream`'s background goroutine (`stream.go:619-620, 690, 697, 428-452`) and read
concurrently, without synchronization, by:
- `Repository.GetStreamURL` (`repository.go:367`) — ranged from Nakama/plugin host goroutines, and
- `Repository.GetUserStreamShare` (`repository.go:402-405`) — the watch-room "join stream" path.
`GetUserStreamShare` even takes `preloadMu` for the `filepath` read (line 396) but then reads the three
scalars lock-free — confirming the locking is incomplete, not absent by design. `cancelStream`
(`stream.go:792`) also mutates `downloadCtxCancelFunc` from a third goroutine. Real-world effect: a
watch-room peer can read a torn selection (URL refreshed but `currentFileId`/`currentTorrentItemId` not,
or vice-versa) → mismatched share; `go test -race` would flag it. Fix: guard the scalars with a small
mutex (or store the active stream as one atomically-swapped pointer).

**[M] Repository-level `previousStreamOptions` is a single global slot on a multi-user server.**
`startStream` writes `s.repository.previousStreamOptions = mo.Some(opts)` (`stream.go:248`) — a single
field on the shared `Repository`. On a networked multi-user server, the last user to start a stream
overwrites it for everyone, and `GetPreviousStreamOptions()` (used by single-host torrentstream/Nakama
features) returns whoever streamed last *globally*. Per-user state moved to `smFor(userID)` but this slot
did not. Either scope it per-user or document it as admin/host-only.

**[L/M] Multi-user prewarm pressure on the TorBox createtorrent budget.** `prewarm.go` runs every
10 min for the admin **plus every active session**, 3 shows each. Steady-state is mitigated by the
selection TTL (24h finished / **1h releasing**), so most ticks are cache hits — but every *currently-
releasing* show a user is watching re-resolves (a `createtorrent`) **hourly per user**. During a busy
season with several users this approaches TorBox's 60/hour `createtorrent` cap; the team already hit
429s and disabled metadata prewarm because of it (`prewarm.go:203-206`). Consider a global
createtorrent rate-limiter / jittered schedule, or skipping prewarm for releasing shows.

**[L] `prewarm.go:18` comment is stale.** It says "refresh before debrid URLs expire (cache TTL is
15m)", but the prewarm loop does **not** refresh URLs — `preloadStream` skips any selection that's
still fresh (`stream.go:928`). URL refresh actually happens lazily on consume at the 2h `urlRefreshTTL`
(`stream.go:1188`). The 10-min interval mostly does nothing once selections are warm.

**[L] `playPreloadedStream` updates the repo slot but not the per-user one.** It sets
`s.repository.previousStreamOptions` (`stream.go:1153`) but not `s.previousStreamOptions`, whereas
`startStream` sets both (`stream.go:247-248`). After consuming a preload, `cancelStream` reads a stale
`s.previousStreamOptions`; only `UserID` is used from it (same user) so it's benign today, but it's an
inconsistency waiting to bite if `prevOpts` is ever read for more than `UserID`.

**[L] TorBox dedup cache hands out a slice it later `append`s into.** `getTorrentsCached` returns the
`t.mylist` reference under lock; `AddTorrent` later `append`s to `t.mylist` under lock (`torbox.go`). If
the append doesn't reallocate (spare cap) while another `AddTorrent` iterates the previously-returned
slice lock-free, that's a backing-array data race. Narrow window; copy-on-return or iterate under lock.

**[+] Strong work.** The latency backoff (`GetTorrentStreamUrl` 500ms→4s vs old fixed 4s), the mylist
TTL cache that releases the lock during the slow fetch (un-serializes concurrent `AddTorrent`), the
cheap URL re-resolve from an existing `torrentItemId` (avoids `createtorrent`), persisted-active-stream
restart reconnect, immutable font cache headers (`serve.go`), and the `httpstream.go` CDN retry
(bounded `MaxConnsPerHost:8`, `Retry-After` honoring with caps, context-aware backoff) are all
well-built and clearly battle-tested.

---

## 3. Profile / multi-user (multi-tenant identity + state plane)
Files: `internal/core/{session,modules,app}.go`, `internal/handlers/{identity,user_auth,status}.go`,
`internal/database/db/{user,session}.go`, `internal/database/models/{models,user_settings}.go`,
`internal/events/{scoped,websocket}.go`.
Commits: 382318fa, 96f8ab41, 37402415, 7786a6f0, …, 52fcf6c4, 98e9d742, 5ca1e85f, 465d2edb, 9b969cc3,
e9be7e61, 76afee6b, f018cad4, 2b602d6f.

This is the most security-sensitive vertical and, reassuringly, the most carefully built. The core
primitives all check out (verified by reading the db layer):
- passwords: **bcrypt** (`DefaultCost`); `hashPassword("")→""` and `CheckUserPassword` rejects an empty
  hash (`user.go:138`), so an empty-password account genuinely cannot authenticate — the comment is true;
- sessions: **256-bit `crypto/rand`** tokens, 30-day expiry **checked and deleted on access**
  (`session.go:41`), plus `CleanupExpiredSessions`;
- `User.PasswordHash` is `json:"-"` — no hash leak through `HandleUserMe`/`HandleUserList`;
- `IdentityMiddleware` cleanly separates the server-password network gate from user identity, and the
  networked-server admin correctly gets its own per-user plane with the `systemUserID = ^uint(0)`
  sentinel (`modules.go:49,77`) to avoid the global-plane "double-claim" that previously stalled admin
  playback;
- per-user AniList cache dirs (`userAnilistCacheDir`) prevent the collection/viewer cache bleed the
  header comment warns about, and I verified the one shared-dir validation client in
  `LoginUserToAnilist` (`session.go:455`) does **not** bleed — `GetViewer` on the raw client bypasses the
  platform-layer disk cache;
- `HandleSaveUserSettings` runs the payload through `ExtractUserOverrides` server-side, so a non-admin
  can't escalate by POSTing admin-only fields.

### Findings

**[L] `SendEventToUserOrUnscoped` leaks per-user streaming events to `UserID==0` clients on a networked
server.** `ScopedWSEventManager.SendEvent` → `SendEventToUserOrUnscoped` (`websocket.go:215`) sends to the
owner's connections **and** every `UserID==0` connection. That fallback is correct for local/desktop
(untagged) clients, but on a password-protected server an anonymous (passed the server password, not yet
logged in) `/events` socket is also `UserID==0` — so it receives other users' `DebridStreamState`
(which carries `TorrentName`) and loader overlays. No cross-leak between *logged-in* users (each is keyed
by id); only the pre-login/anon bucket is a shared sink. Cosmetic + minor metadata leak. Consider routing
truly per-user events through `SendEventToUser` (no unscoped fan-out) once a session exists.

**[L] `UserOverrides.DebridApiKey` is serializable (`json:"debridApiKey"`, `user_settings.go:39`).** Safe
today — handlers expose only a derived `HasApiKey bool` (`status.go:70`) and never `RespondWithData` the
raw overrides — but the plaintext per-user debrid key is one stray return statement away from leaking.
Mark it `json:"-"` and keep exposing only the boolean, mirroring the server-settings pattern.

**[L] `UserSession.Events()` allocates a fresh `ScopedWSEventManager` per call** for non-admins
(`session.go:268`), unlike `ensureModules` which builds one and reuses it. The wrapper is stateless so
this is harmless, just slightly wasteful on a hot path.

**[+] Verdict: well-architected.** The admin-delegate-vs-per-user-session split keeps single-user installs
byte-for-byte unchanged while giving networked installs true isolation; the lazy module construction,
per-user continuity buckets, anon data-less session, and `UserOnly`/`AdminOnly` route gates are all
coherent. No high-severity issues found.

---

## 4. Season grouping (Stremio-style, split-cour merge)
Files: `internal/library/anime/franchise.go`, `internal/handlers/anime_franchise.go`,
`seanime-web/.../anime-library/_lib/group-seasons.ts`, `.../entry/_components/{season-switcher,
merged-season-section}.tsx`.
Commits: 3ed50e85, 0c9703da, fee21776, 9831b527, 64198b4c, 3f44aaa6, ec4ade79, ec72af06, dc82b497,
395b4ec8, d4024486, 2d7c7f63, 51c… (UI-tab move).

### Findings

**[M] Merged-season progress uses the global (admin) collection, not the requesting user's. (CONFIRMED)**
`HandleGetMergedSeason` calls `animeCollection, _ := h.App.GetAnimeCollection(false)`
(`anime_franchise.go:235`) and feeds it to `franchiseEntryProgress` for each cour's `Progress` /
`TotalProgress`. Every sibling library handler uses `sess := h.userSession(c); sess.GetAnimeCollection(...)`
(anime_entries.go:56, anime_collection.go:26, anilist.go:30, …) — this one is the lone outlier. On a
networked multi-user server a non-admin viewing a split-cour merged season sees the **admin's** per-cour
progress, and their own progress is ignored (so "resume" / watched-count on the merged view is wrong for
them, and it leaks the admin's progress). Fix: `sess := h.userSession(c); animeCollection, _ :=
sess.GetAnimeCollection(false)`. (`auto_downloader.go` and `metadata.go` also use the global collection,
but those are server-scoped features, so they're fine.)

**[L/M] A transient AniList failure poisons the franchise-group cache for 24h.** `resolveFranchiseGroup`
ignores the `FetchMediaTree` error (`anime_franchise.go:107`); a partial/failed walk yields just the root
member, `BuildFranchiseFromMembers` builds a 1-season group, and `CacheFranchiseGroup` stores it for 24h
under that id. So a user who first opens an entry during an AniList hiccup gets an un-grouped view that
"sticks" for a day. The franchise-*ref* cache correctly avoids caching transient failures
(`franchise.go:445`); the franchise-*group* cache should do the same (skip caching when the tree walk
errored or returned a suspiciously small member set).

**[L] Empty-TMDB refs are cached for 30 days (`franchise.go:451`).** A show with no animap→TMDB mapping
yet (common for brand-new entries) caches an empty `FranchiseRef`, so it won't group until the 30-day TTL
lapses even after the mapping appears. Acceptable, but a shorter TTL for empty results would self-heal
sooner.

**[L] The title-stem heuristic is duplicated in Go and TS with a subtle divergence.** Go's
`FranchiseTitleStem` strips a trailing roman numeral via `\s(ii|iii|iv|v|vi|vii)\b` (matches at
end-of-string); TS's `franchiseTitleKey` uses `/\s(ii|iii|iv|v|vi|vii)\s/g` (requires a following space),
so a title ending in " II" stems on the server but not the client. Both also stop at "vii" (no
viii/ix/x). Grouping is TMDB-id-first so the title fallback rarely decides, but the two should match —
ideally the client consumes the server's stem rather than re-implementing it.

**[+] Good.** The presentation-only overlay (each season stays its own AniList entry — tracking
untouched) is the right call; the generic `collapseBy` is cleanly reused across library/lists/search/
discover; per-member group caching makes sibling-season views instant; `isExtra`/`FranchiseTag` format-vs-
relation classification and the TMDB-or-stem relation-walk bound (`anime_franchise.go:172`) are
thoughtfully handled.

---

## 5. Nakama same-instance watch rooms
Files: `internal/nakama/watch_room.go` (+`_test.go`), `internal/handlers/nakama_rooms.go`,
`seanime-web/.../nakama/{nakama-room-sync.ts,nakama-manager.tsx}`.
Commits: b107c559, c4bef93d, 0ba4ab74, 5b73584f, 4a72c4e6, 5e8bc095, c97ce64a, 6fe58a1b, 3defc1f3.
(This is the newest feature and the last 8 commits are all room fixes — it is still stabilizing.)

### Findings

**[H] The live `*WatchRoom` (with its `Participants` map) is JSON-marshaled outside `room.mu` while
membership ops mutate the map under the lock → fatal "concurrent map read and map write".** Two paths
marshal the live room without holding its lock:
- `broadcastRoomState` collects client ids under `RLock`, unlocks, then calls
  `SendEventTo(cid, NakamaWatchRoomState, room, true)` per member (`watch_room.go:792-794`) — each send
  re-marshals the live `room`;
- the HTTP handlers `RespondWithData(c, room)` after `CreateRoom`/`JoinRoom` (`nakama_rooms.go:91,118`).

Meanwhile `JoinRoom`/`LeaveRoom`/`HandleClientDisconnect` do `room.Participants[k]=…` / `delete(…)` under
`room.mu.Lock()`. Because the marshal doesn't take the lock, a join/leave concurrent with a broadcast or
an HTTP response is a concurrent map iteration+write — which the Go runtime turns into an **unrecoverable
fatal crash** (not a catch-able panic). High-confidence by inspection; reproducible under `go test -race`
with a couple of goroutines hammering `JoinRoom`/`LeaveRoom` while another calls `broadcastRoomState`. The
single-threaded unit tests don't exercise it. Fix: marshal a snapshot built **under the lock** — a
`RoomState` DTO (like the existing `RoomCard`), or copy `Participants` into a fresh map — and send/return
that, never the live struct. (Pointer fields `CurrentMediaInfo`/`LastPlayback` have the same
read-while-write hazard, fixed by the same snapshot.)

**[L/M] Zombie rooms are never reaped.** `HandleClientDisconnect` deliberately does not remove a
participant (to allow reconnect), and only the *explicit* `LeaveRoom`/host-leave deletes a room. If every
member's client simply drops (tab close, network loss) without an explicit leave, the room lingers
forever with `PlaybackActive` still true and dead `ClientID`s — still ticked every second by the
broadcast loop and still advertised in `ListRooms` (joinable ghost). There is no idle/empty-of-live-
clients TTL. Add a reaper that closes a room once it has had no live client for a grace period.

**[L] Temporary debug instrumentation shipped enabled.** The follower apply path sends a
`NAKAMA_ROOM_DEBUG` message to the server **on every applied sync** (`nakama-room-sync.ts:318`), i.e.
~once/second per follower during steady playback (the server ticker drives a reconcile every
`broadcastTickMs`). Commit c97ce64a ("client->server debug logging") is explicitly diagnostic. Strip the
`NAKAMA_ROOM_DEBUG` round-trip (and the matching server log) before this is considered done.

**[L] Discovery cards go stale on what a room is playing.** `RelayPlaybackStatus` updates
`room.CurrentMediaInfo` but never calls `broadcastRoomsUpdated`, so the `ListRooms` cards (which surface
`MediaId`/`EpisodeNumber`) don't refresh until the next join/leave. Emit a (debounced) rooms-updated when
the playing media changes.

**[L] `resolveRelay` does double work in production.** `RelayPlaybackStatus` calls it only for the
`allowed` bool and then recomputes the target list itself (`watch_room.go:551` vs `591-596`). The function
is "pure for testability", but in the hot path its target computation is dead. Harmless, just wasted work.

**[+] The sync model is well-designed.** Server-authoritative position (`currentPositionLocked` =
position + elapsed), sender-excluded broadcasts so self-feedback is structurally impossible, controller-
only heartbeats (`nakama-room-sync.ts:256`), the driver never reconciling to its own echo, state-matched
echo suppression, one process-lifetime ticker, idempotent join/leave, host control-reclaim, and control
handoff on disconnect are all sound. The membership/control logic has good unit tests; the gap is purely
the concurrency around serializing room state.

### Status — implemented 2026-06-24 (backend `go build ./...` + `go vet` clean; `nakama` tests pass normal **and** under `-race`)

- **[H] concurrent-map crash — FIXED.** Added `WatchRoom.snapshotLocked()` / `Snapshot()` (fresh
  `Participants` map of value-copied participants; pointer fields are replaced wholesale so sharing
  them is race-free). `broadcastRoomState` now marshals a snapshot taken under `RLock`, and the
  `CreateRoom`/`JoinRoom` HTTP handlers return `room.Snapshot()` — the live struct is never
  serialized. New `TestWatchRoom_ConcurrentSnapshotMarshal` hammers `JoinRoom`/`LeaveRoom` while
  marshaling snapshots and passes under `-race`.
- **[L/M] zombie rooms — FIXED.** Added a reaper (`reapIdleRooms`, run once per broadcast tick):
  closes a room that has had no connected client (`wsEventManager.GetClientIds()`) for
  `roomIdleTTL = 2m`. `WatchRoom.lastLiveAt` tracks liveness; reconnects within the window are
  safe. Pure core split into `reapIdleRoomsWith` + unit test `TestWatchRoom_ReapIdleRooms`.
- **[L] stale discovery cards — FIXED.** `RelayPlaybackStatus` now fires `broadcastRoomsUpdated`
  when a room's advertised media actually changes (start / episode switch), not on every heartbeat.
- **[L] `resolveRelay` double work — FIXED (prior session).** Now `_, allowed := room.resolveRelay(…)`;
  the discarded target computation is gone.
- **[L] debug instrumentation — DEFERRED on purpose.** The `NAKAMA_ROOM_DEBUG` round-trip is still
  load-bearing for verifying the not-yet-deployed playback fix (see `nakama-audit.md`); strip it
  once the watch-room playback is confirmed working live.

(Playback/sync correctness — the "follower never plays" bug, per-peer link contention, opt-out
wedge, multi-source echo — is a separate audit + set of fixes in `nakama-audit.md`.)

---

## 6. Events scoping, UI/navigation, settings gating, logging, misc
Files: `internal/events/*`, `internal/handlers/routes.go`, `internal/core/session.go`
(`ResolveDirectStreamManagerWithAttachment`), web `main-sidebar.tsx`, settings containers.
Commits: 93350f52, 31101ac5, f94f5eca, f94feeb (integrations), 9dd12d97, f4ace33a, b3b41bf8,
d4917d8e/82308c5b/… (settings role-gating), 52fcf6c4.

### Findings

**[L] Font attachments route across users by filename only.** `ResolveDirectStreamManagerWithAttachment`
(`session.go:313`) returns the first session whose active stream contains a font with the requested name.
Anime releases very commonly bundle identically-named fonts (e.g. `Arial.ttf`, `OPSans.otf`); with two
users streaming different releases concurrently, a font subresource request (which carries no user
session or `?id=`) can be served from the *other* user's stream. Harmless when the bytes match, but a
same-name/different-bytes font renders subtitles with the wrong typeface. The `?cv=<size>` content-
versioning added for caching doesn't fix routing. Low impact; a per-stream attachment token in the URL
would make it deterministic.

**[+] Role gating is enforced server-side, not just hidden in the UI.** `/settings`,
`/settings/auto-downloader`, `/settings/media-player`, `/mediastream/settings`, `/torrentstream/settings`,
`/debrid/settings` are all `AdminOnly` (`routes.go:178-182, 466, 497, 570`); debrid/torrent *operations*
are `UserOnly`; stream *starts* call `guardStreamingUser` inline (`debrid.go:365`, `directstream.go:25`).
So the settings-tab gating and decluttered sidebar are cosmetic layers over a real server-side boundary —
the right way round. The username-on-every-log-line enrichment and the "don't open `/events` on
splashscreen" fix are benign.

### Working-tree note (not part of the committed history)
The repo has **uncommitted** changes: `ResolveExpectedSeason` being wired into the torrent auto-download
path (`autoselect.go`) + its exported `SeasonNumberFromMetadata` (`franchise.go`) — consistent extensions
of committed work — and an in-progress "announcement / stale-client-notice" feature
(`updater/*.go`, `handlers/{docs,releases}.go`, `stale-client-notice.tsx`). These are WIP and were not
audited as commits; they compile (`go vet` clean) but should be committed/finished or stashed.

---

## 7. Latency & prewarm efficiency (optimization opportunities)
Files: `internal/debrid/torbox/torbox.go`, `internal/debrid/client/{stream,finder,repository}.go`,
`internal/core/prewarm.go`, `internal/directstream/{manager,debridstream,httpstream}.go`,
`internal/torrents/autoselect/{search,file_selection}.go`.

This section is forward-looking (where to cut time + TorBox calls), not bug findings.

### Critical-path TorBox call map (cold AutoSelect debrid play, cached release)
Every numbered item is a sequential network round-trip to the TorBox API before the player gets a URL:

| Stage | Call | Endpoint | Notes |
|------|------|----------|-------|
| **Select** | `postSearchSort` → `GetInstantAvailability` | `/checkcached` (`list_files=true`) | cache ranking; returns each cached release's **file list** |
| **Select** | `selectFileFromDebrid` → `GetTorrentInfo` | `/checkcached` (`list_files=true`) | **re-fetches the winner's file list** (already had it ↑) |
| **Add** | `AddTorrent` → `getTorrentsCached` | `/torrents/mylist` | dedup; 120s cache, lock released during fetch ✓ |
| **Add** | `AddTorrent` (new only) | `/torrents/createtorrent` | **the 60/hr-limited call** + a 500ms pre-call sleep |
| **Download** | `GetTorrentStreamUrl` poll | `/torrents/mylist?id=` | every 500ms→4s; ready-on-first-poll for cached |
| **Download** | `GetTorrentDownloadUrl` → `getTorrent` | `/torrents/mylist?id=` | **re-fetches the torrent** just to map filename→file id |
| **Download** | `GetTorrentDownloadUrl` | `/torrents/requestdl` | the actual URL; preceded by a flat **1s settle sleep** |

So a *cached* play makes ~5-6 serial TorBox round-trips plus ~1.5s of fixed sleeps before the player even
starts loading metadata. Two of those round-trips and most of the sleeps are removable.

### Per-stage time reductions

**[Redundant call] `GetTorrentDownloadUrl` re-fetches the torrent.** The poll loop already fetched the
torrent via `GetTorrent(opts.ID)` (`torbox.go:333`), but `GetTorrent`→`toDebridTorrent` **drops the
`Files`** (`torbox.go:588` has no Files field), so `GetTorrentDownloadUrl` calls `getTorrent(id)` again
(`torbox.go:382`) only to map the filename → numeric file id. Thread the poll's raw `*Torrent.Files`
(or the resolved numeric id) through and this whole `/mylist?id=` round-trip disappears from **every**
play and every prewarm-consume URL refresh.

**[Redundant call] `GetTorrentInfo` duplicates the instant-availability file list.** `postSearchSort`
already pulled `list_files=true` for the winner (`finder.go:95`), then `selectFileFromDebrid` pulls the
same `/checkcached` file list again (`file_selection.go:258`). Thread the cached release's `CachedFiles`
into `selectFile` and skip `GetTorrentInfo` for cached torrents (keep the `/torrentinfo` fallback only for
uncached). Removes one `/checkcached` per cached play/preload.

**[Fixed sleeps ≈1.5s] Trim the settle + first-poll delays.** `GetTorrentStreamUrl` sleeps a flat **1s**
before `requestdl` (`torbox.go:344`, "drop if the URL never 404s") and waits **500ms** before its first
readiness poll (`torbox.go:326,332`). For a cached release both are pure latency. Make the first poll
immediate and request the URL right away, retrying on 404/not-ready with a short backoff (the
`httpstream.go` CDN retry already tolerates transient errors downstream). ~1.5s off the common path.

**[Metadata handoff] Re-enable metadata prewarm for the single highest-probability next-up.** The
continue-watching prewarm sets `PrewarmMetadata:false` (`prewarm.go:206`) because the font/header
downloads bursting the CDN caused 429s. But `PrewarmStreamMetadata` is already careful (caches the parser
by URL, primes the content-type HEAD, and warms only ~6s of the start region — `manager.go:137`), and the
CDN proxy is now concurrency-bounded (`MaxConnsPerHost:8`) with immutable font caching. Prewarming
metadata for just the **one** most-likely next episode (not all 3 shows) would make the most-probable play
skip the "Loading metadata…" step without re-creating the burst.

**[Select] Resolution fallback is sequential.** `searchFromProvider` loops `profile.Resolutions` in order,
re-searching when a resolution yields nothing (`search.go:169`). The common case (first resolution has
results) is fine; the multi-resolution fallback pays a full extra search round (×AIOStreams' ~10s). If the
fallback matters, search the resolutions concurrently and let scoring pick, rather than serially.

### Prewarm: more episodes, fewer TorBox calls, higher consistency
The createtorrent budget (60/hr) is the hard ceiling, and prewarm is per-user × 3 shows on a 10-min
ticker — so cutting createtorrent calls is what unlocks prewarming *more* episodes.

**[Biggest lever] Resolve at the release/batch level, not per-episode.** Today each episode preload runs
its own search + analyze + `AddTorrent`. When the next K episodes live in one **season-batch** release,
you can search once, `AddTorrent` once (**1 createtorrent**), and derive each episode's URL (cheap
`requestdl`) from the same `torrentItemId` — the analyzer already maps every file→episode and
`BatchEpisodeFiles` already exists. A batch-aware prewarm keyed by `(infohash)` that fans out per-episode
URLs would prewarm K episodes for ~1 createtorrent instead of K, turning the binge-ahead case nearly free.

**[Consistency] Persist the prewarm selection set to the DB, not just the one active stream.**
`persistActiveStream` already snapshots the single active stream for restart reconnect (`stream.go:1094`);
extend it to the priority continue-watching set — persist `{mediaId, episode, infohash, torrentItemId,
fileId, urlResolvedAt}` so a restart or a fresh session reuses the selection (no re-search/re-add). Prewarm
then survives restarts (consistency) and costs zero createtorrent on warm reuse.

**[Cut createtorrent for releasing shows] Decouple URL-refresh from re-search.** The 1h releasing-show
selection TTL (`preloadSelectionTTLReleasing`) forces a re-search + possible re-add **hourly per releasing
show per user** — the dominant createtorrent driver under multi-user load. The URL can be kept fresh from
the existing `torrentItemId` for cents (no createtorrent); only re-*search* when the user actually advances
an episode, or on a much longer cadence (6-12h). This frees most of the budget that 1h re-resolves consume.

**[Coverage] With the budget freed, prewarm next-up *and* next-next.** Once episodes inside an
already-added batch cost ~0 createtorrent (lever 1), extend the target set from "next-up of 3 shows" to
"the next 2-3 episodes of each in-progress show" for true binge-ahead instant starts.

**[Minor] Coalesce availability checks across a prewarm cycle.** `getTorrentsCached`'s 120s TTL already
shares the mylist across a cycle's `AddTorrent`s; the per-target `GetInstantAvailability`/`GetTorrentInfo`
checkcached calls are not coalesced, but the redundant-call fixes above matter more.

**Combined effect:** removing the two redundant round-trips + ~1.5s of sleeps cuts the cached cold-start by
roughly half its TorBox overhead; batch-level resolution + persisted selections + decoupled refresh cut
steady-state createtorrent calls enough to widen prewarm from 3 next-up episodes to a multi-episode
binge-ahead window within the same 60/hr ceiling.

---

## 8. Tenji iOS/RN client (`ClinShaiju/seanime-tenji`)
22 commits since fork `84ccb152` (from `5rahim/seanime-tenji`). A thin Expo/RN client that ports the same
fork features (Nakama rooms, profile login, season-grouping, debrid prewarm + reconnect) against the same
server. Most logic lives server-side, so Tenji inherits the server findings (notably the merged-season
admin-collection bleed in §4 affects Tenji's merged-season view identically) and adds little new surface.

### Findings

**[M] Season-grouping toggle read-location drift.** Tenji reads
`serverStatus?.settings?.library?.groupSeasons` (`group-seasons.ts:155,176,205,232,268`), but the canonical
per-user toggle was **moved to the Theme** by commit 2d7c7f63 ("move season grouping to the User Interface
tab (theme, per-user)"), and seanime-web reads/writes `themeSettings.groupSeasons`. The server still carries
a legacy `LibrarySettings.GroupSeasons` (`models.go:144`) **and** the live `Theme.GroupSeasons`
(`models.go:467`). If a user enables grouping from the web UI (writes the Theme field) but Tenji reads the
Library field, Tenji's grouping silently won't reflect it. Verify Tenji's own settings write path and align
Tenji to `themeSettings.groupSeasons`, or have the server mirror the two.

**[L] Inherited temporary debug logging.** Tenji's HEAD is the same "client->server debug logging
(temporary)" commit — the per-apply `NAKAMA_ROOM_DEBUG` round-trip exists here too
(`src/lib/nakama/use-watch-room-sync.ts`). Strip it alongside the server-side and web one (§5).

**[L] Same Go/TS stem-regex divergence (§4) is duplicated here.** `franchiseTitleKey`
(`group-seasons.ts:38`) uses the surrounding-space roman-numeral pattern, so it diverges from the Go
`FranchiseTitleStem` the same way seanime-web does. Ideally all three consume the server's stem.

**[+] Faithful ports.** The room sync (controller-only heartbeat, state-matched echo suppression), debrid
prewarm/reconnect, and season-grouping mirror the seanime-web implementations closely, so they carry the
same (good) correctness profile rather than re-deriving it. The hand-synced generated API in
`src/api/generated/*` is the main drift risk over time — keep it surgically in sync with the server.

---

## 9. AIOStreams fork (`ClinShaiju/AIOStreams`) — where search time goes
Only 3 commits diverge from upstream `Viren070/AIOStreams` (merge-base `004e0e8f`), all in the Seanime
extension + minor parsing: direct-URL streams, parallel multi-id search, size-token stripping, SeaDex
dual-audio language surfacing. The fork is thin and the extension is already well-optimized — so the time
lives in the **upstream aggregation pipeline**, which (since you own the fork) you can still tune.

### Where the ~10s goes (traced)
1. **Extension** (`aiostreams-torrent-provider/main.ts`): one serial `aiostreams.anime(id)` resolve
   (anilist/kitsu → imdb/kitsu ids), then **parallel** `Promise.all` of one `aiostreams.search()` per
   preferred id type (`main.ts:102,109`). Good — the id-type searches don't serialize.
2. **Each `search()`** hits the server, which runs `getStreams` (`resources.ts:595`) →
   `ctx.fetcher.fetch(addons)` (`fetcher.ts`). The fetcher fans out **all** supported addons in
   **parallel** (`Promise.all(addons.map(fetchFromAddon))`, `fetcher.ts:238`), so the response blocks on
   the **slowest addon up to its timeout** — AnimeTosho being the ~10s tail.
3. `AnimeTosho`'s timeout defaults to **`null`** (`builtins.ts:551`) → it inherits the generic max timeout,
   i.e. there is no anime-scraper-specific cap by default.

### Levers (highest impact first — all config, no fork-code change)

**[1] Enable `dynamicAddonFetching` with an exit condition.** The fetcher already supports early exit
(`fetcher.ts:270`): when `userData.dynamicAddonFetching.enabled` with a `condition` (it parses
`totalTimeTaken`/stream-count predicates via `ExitConditionEvaluator`), it resolves as soon as the
condition is met and tags the still-running slow addons `cut_off`. A condition like "exit once there are
≥N cached streams, or `totalTimeTaken > 4000`" returns the fast cached results immediately and stops
waiting on AnimeTosho's tail. **This is the single biggest win and needs no code change.**

**[2] Cap the slow scraper.** Set `BUILTIN_DEFAULT_ANIMETOSHO_TIMEOUT` (currently null) to e.g. 5-6s so
even without dynamic fetching the tail is bounded. Same for any other slow anime indexer.

**[3] Make sure anime addons declare `mediaTypes`.** The fetcher already drops addons whose `mediaTypes`
exclude the query type (`fetcher.ts:108-130`); ensure your generic/non-anime addons are typed so they're
not queried (and waited on) for anime ids — fewer addons in the parallel set = shorter tail.

**[4] Tune the per-addon `streamsCache` TTL.** There's a per-addon stream cache (`wrapper.ts:79`) with a
`cacheTtls` map keyed by preset/hostname/`*`. Set the anime indexers' TTL long enough that Seanime's
prewarm → actual play (and next-episode prewarm) of the same id+episode land on the cache — turning the
second search into a near-instant cache hit.

**[5] Lean on `precacheNextEpisode`.** AIOStreams will background-resolve **and ping** (`pingStreamUrls`)
the next episode's debrid streams after each request when `userData.precacheNextEpisode` is on
(`resources.ts:694`). Enabling it complements Seanime's own prewarm — the AIOStreams side warms the
upstream + debrid CDN, the Seanime side caches the resolved selection.

**[6] (fork code) Cache the extension's `anime()` resolution.** The one serial prefix before the parallel
fan-out is `aiostreams.anime(id.type, id.value)` (`main.ts:102`) — a stable anilist→id mapping per media.
If the goja runtime exposes any persistence (`$store`-style) or even a module-level `Map`, caching it
removes one RTT from every search (including the prewarm path).

**Bottom line:** the fork code is fine; the ~10s is upstream-aggregation tail latency, and **lever [1]
(dynamic fetching) plus [2] (AnimeTosho timeout cap) collapse it** without touching the aggregator — they
let the response return on the fast cached addons instead of waiting for the slowest scraper.

---

## Verification performed
- `go vet ./internal/{torrents/autoselect,debrid/client,nakama,core,library/anime,handlers,events}/` →
  **clean (exit 0)**.
- `go test ./internal/torrents/autoselect/` → **pass**; a focused probe **empirically confirmed** the
  multi-subtitle→English-dub misclassification (§1) before being removed.
- The data races (§2, §5) and the merged-season collection bleed (§4) were established by reading the
  concrete writer/reader code paths; the races would additionally surface under `go test -race`.
- §8 (Tenji) and §9 (AIOStreams) were audited by reading the actual sources at `H:/Projects/seanime-tenji`
  and `H:/Projects/AIOStreams`; the Tenji `groupSeasons` drift was confirmed against the server's two
  `GroupSeasons` fields (`LibrarySettings` vs `Theme`), and the AIOStreams dynamic-fetch / timeout levers
  against `fetcher.ts` + `builtins.ts`.
