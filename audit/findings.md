# Findings

Two audit passes (24 disjoint sweep agents each) + hand-verification of the security-critical and highest-impact items. **Confirmed** = an adversarial refuter (or I, directly) traced a concrete failure in the code at HEAD `fd2ffc25`. **Plausible** = traced in code by the sweep but its independent re-verify was lost to a transient API outage; where the root cause was proven elsewhere I say so.

Known/prior-audit items (`fable-audit.md`, nakama same-user collision, WriteAndFlush noise, mpv-prism WGL, Coordinator-nil `57ad877d`, deploy-502) were excluded by construction.

Severity counts: **4 critical, 2 high, 14 medium, 4 low**, + 1 design-decision conflict. One finding was refuted and dropped.

**Verification status.** Two adversarial-verify passes ran; a transient API outage killed 12 verifier calls in the second pass. All 6 of those that were code-correctness claims are now verified: 4 by me directly (C2, H1, M3a, M3b), 2 via a non-failed sibling verifier (M6a, M6c). The other 6 killed verifiers were upstream **issue-triage** items (`#490`/`#227`/`#378`/`#738`/`#679`/`#868`) — "does this gap exist in our fork" grep checks answered during the sweep, not fork-correctness claims; see `upstream-issues.md`. Items still marked **PLAUSIBLE** below (M2c only) were traced in code but not independently re-verified.

---

# ★ Systemic root cause — the auth boundary is a no-op on this deployment

`isTrustedRequest(req, serverPassword)` **returns `true` whenever a server password is set** (`local_security.go:279-281`):

```go
func isTrustedRequest(req *http.Request, serverPassword string) bool {
    if serverPassword != "" || security.IsLax() { return true }   // ← this deployment
    ...
}
```

Every "privileged" guard delegates to it — `canUsePrivilegedExtensionManagement` (`:372`), `guardPrivilegedLocalExecution` (`:444`), and the settings/status handlers rely on client-side tab-hiding instead of a server check. The design assumes "has the server password ⇒ trusted operator." But your deployment is a **networked multi-user server where the shared password is the entry ticket for every regular user and every browse-only anon.** So the whole privileged tier collapses to "any password holder can do anything." This is the throughline behind C1–C4 below. **Fix them as a set:** gate these routes on real admin identity (`h.AdminOnly`), not `isTrustedRequest`.

I verified `isTrustedRequest`, `canUsePrivilegedExtensionManagement`, `guardPrivilegedLocalExecution`, and the route table by hand — all confirmed.

---

# CRITICAL

## C1 — Extension install/uninstall/edit → remote code into the shared plugin sandbox, by any password holder · CONFIRMED (hand-verified)
`POST /api/v1/extensions/external/{install,install-repository,uninstall,edit-payload,reload,disabled}` carry **no `AdminOnly`** (`routes.go:514-539`); each handler guards only via `guardPrivilegedExtensionManagement` → `canUsePrivilegedExtensionManagement` → `isTrustedRequest` → `true` when a password is set. So every logged-in non-admin, and any browse-only anon who knows the shared password, can **install an extension from an arbitrary manifest URL** (remote code into the Goja/plugin sandbox affecting all users) or uninstall/disable others' extensions. Contradicts the fork's admin-managed-extensions design and the anon-browse-only guarantee (`f018cad4`).

- **Fix:** add `h.AdminOnly` to every `/extensions/external/*` write route.

## C2 — Server self-update + release download → binary swap & restart, by any password holder · CONFIRMED (hand-verified)
`POST /api/v1/install-update` and `/download-release` (`routes.go:341-342`) carry no `AdminOnly`; handlers gate only on `guardPrivilegedLocalExecution` → `isTrustedRequest` → `true` when a password is set. Any password holder (regular user or anon) can trigger a binary self-update + process restart, or force the host to download release files — an integrity/availability vector (forced downgrade, restart-loop DoS) that must require admin.

- **Fix:** add `h.AdminOnly` to `/install-update`, `/download-release`, `/download-mac-denshi-update`.

## C3 — All server secrets leak via `GET /settings`, `GET /status`, `GET /debrid/settings` · CONFIRMED (hand-verified)
Three unauthenticated-beyond-password read paths return DB credentials in cleartext to any non-admin/anon:

- `GET /debrid/settings` (`routes.go:571`, no `AdminOnly`) → `HandleGetDebridSettings` returns `models.DebridSettings.ApiKey` (`debrid.go:24-31`, `models.go:630`) — the **paid TorBox key**, the primary shared credential.
- `GET /settings` (`routes.go:177`, no `AdminOnly`) → `HandleGetSettings` returns the full settings clone; `VirtualizeSettingsPaths` only rewrites iOS paths, **no secret redaction** (`settings.go:24-38`). `models.Settings` embeds `Nakama`, `Torrent`, `MediaPlayer` (`models.go:85-97`), exposing `Nakama.HostPassword` + `RemoteServerPassword` (**the server password itself**), `QBittorrentPassword`, `TransmissionPassword`, `VlcPassword`, `VcTranslateApiKey`.
- `GET /status` → `NewStatus` redacts **only** `DebridSettings.ApiKey` for non-admins (`status.go:175-179`) but still returns `Settings.Torrent` (qBit/Transmission passwords), `MediastreamSettings` (ffmpeg/ffprobe paths), `TorrentstreamSettings` (download dir/host). The code comment admits it relies on the client hiding these behind admin tabs — not a wire boundary.

The redaction was bolted onto one field in one handler. **Fix:** a single server-side secret-scrubbing projection applied to every settings/status response for non-admin sessions (or gate the raw routes `AdminOnly` and give clients a non-secret projection).

## C4 — Nil `playbackCtx` → `context.WithCancel(nil)` panics the whole server · CONFIRMED
`releaseCurrentStreamLocked`/`discardCurrentStreamLocked` set `m.playbackCtx = nil` (`stream.go:310,380`) while in-flight range handlers and the SeekedEvent path read it **without `playbackMu`** and pass it to `StartSubtitleStreamP`, whose `context.WithCancel(playbackCtx)` has **no nil guard** (`subtitles.go:377`). `httpstream.go:460` spawns the subtitle kick in a **bare goroutine with no `recover`**, so hitting the window during an episode switch concurrent with a seek storm **panics the entire multi-user process**. The codebase nil-guards this same field at four other read sites (`torrentstream.go:234`, `httpstream.go:357`, `httpstream_warm.go:119`, `subtitles.go:289`), proving nil is a known reachable state — the subtitle-kick paths were missed. Upstream code (`8b5c6bba`), present since the v3.9.0 merge; not in `fable-audit.md`.

- **Fix:** nil-guard `playbackCtx` in the subtitle-kick paths (mirror the existing guards) and wrap the `httpstream.go:460` goroutine in `recover`.

---

# HIGH

## H1 — Watch-room sync is dead when Denshi plays via MpvCore (the default player since v3.9) · CONFIRMED (hand-verified)
`useWatchRoomPlayerSync` drives a raw `HTMLVideoElement` from `vc_globalVideoElement` and `nativePlayer_stateAtom` (`nakama-room-sync.ts:79-84`). `vc_globalVideoElement` is written **only** by VideoCore's global bridge (`video-core.tsx:211`); MpvCore playback flows through `WSEvents.MPVCORE` into separate `mc_*` atoms and **never** touches it (grep-confirmed: zero MpvCore writers). So a Denshi-on-MpvCore room member:
- as **driver**, emits nothing (`videoElement` null → no heartbeat/play/pause/seek/stop → followers never auto-start);
- as **follower**, applies nothing (`if (!videoElement) return`, `:385`).

The hook's own debug instrumentation (`:130`, `:362-368` — "video:false ... otherwise invisible") shows this was already being chased. Nakama watch rooms are a core feature and MpvCore is Denshi's default → the feature is effectively non-functional for the primary client.

- **Fix:** bridge MpvCore state (`mc_*`/`mpvCore_stateAtom`) into a player-agnostic sync surface, or add an MpvCore path to `useWatchRoomPlayerSync` alongside the video-element one.

## H2 — Browser direct-play only works for H.264+AAC (malformed codec strings) · upstream [#866], CONFIRMED in code
`internal/mediastream/videofile/info.go:398`: `ret += fmt.Sprintf(".L%02X.BO", stream.Level)` for HEVC uses a **hex** level and a literal letter **O** instead of digit 0 (should be decimal + `.B0`), so HEVC never satisfies the browser's `canPlayType()` and **always falls back to transcode**. AV1 level and Opus/VP9 mappings share the class of bug. Affects local-library browser playback of any non-H.264 file (not the primary debrid path → high, not critical for this deployment).

---

# MEDIUM

## M1 — Cached-vs-batch & language ranking

- **M1a — Size tie-break assumes bigger = higher bitrate, false for batches** · CONFIRMED. `bc317d74`'s tie-break `cmp.Compare(b.Size, a.Size)` (`comparison.go:646-650,705-709`) fires when a multi-episode batch ties a single episode in the same tier (neutral `BatchPreference` contributes 0); the batch's Size reflects N episodes, so it always wins on episode count, inverting the intent. No test covers batch-vs-single ties.
- **M1b — `RequireLanguage` filter ignores flag-emoji languages** · CONFIRMED. The filter uses `parsed.Language` + a bounded name match, never `c.flagLanguages` (`comparison.go:532-556`), while ranking (`:348`) does. `CleanReleaseName` strips flag emoji before parsing (`release_name.go:15`), so a flag-only AIOStreams release (`🇬🇧🇯🇵`) has empty `parsed.Language` and is dropped under `RequireLanguage=true` despite matching flags. Name-match fallback is in the `else` branch, so it also never runs when one non-matching language parsed.
- **M1c — Background preloads emit user-visible auto-select status, wired to the admin-scoped plane** · CONFIRMED (regression). Upstream v3.9.0 made `FindBestTorrent` unconditionally `SendEvent(StreamAutoSelectStatus)` (`autoselect.go:189-207`), wired via `opts.WSEventManager = a.adminEvents` (`repository.go:140`, `modules.go:346`). Two problems: (1) on a networked server non-admins **never** receive the pill's searching/ranking/candidate feed (it goes only to admin connections), while (2) the admin receives **every** user's auto-select status incl. candidate torrent names, and background preloads (silent by contract) flash the pill for episodes nobody pressed play on. **Fix:** route via the per-user `s.ev(opts)` pattern and thread a silent flag through preload callers.

## M2 — Per-user isolation gaps (post multi-user)

- **M2a — `Repository.GetStreamURL()` returns a non-deterministic user's stream** · CONFIRMED. After `9b969cc3` split StreamManagers per-user, `GetStreamURL()` still returns "first non-empty" by **ranging a map** (`repository.go:490-503`) — randomized order. Callers assuming one active stream (nakama relay `nakama.go:326,372`, plugin `debrid.go:423`) get an arbitrary pick with 2+ concurrent streamers. **Fix:** resolve by the caller's userID.
- **M2b — Per-session `PlaybackManager`s clobber the shared `mediaplayer.Repository` subscription** · CONFIRMED. Every PM subscribes with fixed id `"playbackmanager"` (`playback_manager.go:282`); `Subscribe` does `Set(id, sub)` (`repository.go:162`), so each new session steals the external-player event stream and orphans earlier listener goroutines forever. Exact pattern the fork fixed for VideoCore in `465d2edb` (`"videocore:u%d"`) — PM was missed. Latent here (no server-side external playback).
- **M2c — `cancelStream` with no prior stream falls back to the admin/global directstream manager** · PLAUSIBLE. When `previousStreamOptions` is unset (fresh per-user manager, notably post-restart — `loadPersistedActiveStream` never sets it), `s.ds(nil)` returns the **global/admin** manager (`stream.go:300-306`); `CloseOpen("")` aborts what's preparing on the admin's manager. `CancelStreamOptions` already carries `opts.UserID`. **Fix:** resolve via `r.dsFor/evFor(opts.UserID)` when `prevOpts` is nil. _(Distinct from the refuted `GetProvider` early-return item — see bottom.)_

## M3 — MpvCore/VideoCore atom split (same root cause as H1)

- **M3a — Debrid auto-resume after server restart never arms for MpvCore** · CONFIRMED (hand-verified). `useDebridReconnectResume` (`cbeb92c6`) watches only `nativePlayer_stateAtom` (`handle-debrid-reconnect.ts:3,24`) and is mounted only in `native-player.tsx:31`; MpvCore runs through `mpvCore_stateAtom`, so `streamActive` is always false and a mid-play server deploy/crash kills MpvCore playback with no re-issue of the last `DebridStartStream`. **Fix:** also watch the MpvCore state, or mount the hook player-agnostically.
- **M3b — Global nakama Manager binds the admin's `MediacoreCoordinator`** · CONFIRMED (hand-verified). `nakama.NewManager` is built once with the app-level `a.MediacoreCoordinator` (`modules.go:418-429`), but each per-user session builds its own `s.mediaCoord = mediacore.NewCoordinator(...)` (`session.go:167`). Upstream watch-party host/player-control paths therefore see/drive only the admin session's player; non-admin playback is invisible to watch-party hosting. **Fix:** resolve the acting user's session Coordinator per watch-party operation.

## M4 — Playback session lifecycle

- **M4a — Per-session MpvCore/Coordinator goroutines never stopped on session eviction** · CONFIRMED. `57ad877d` gives every session its own MpvCore + Coordinator, each spawning a bare `range`-over-channel listener (`mpvcore.go:424`, no stopCh). `a.sessions.Delete(userID)` on relink/logout (`session.go:523,546`) does **no** `Stop()`/`Unsubscribe` — goroutines park forever. Eviction is automatic: `logoutUserFromAnilist` is the AniList invalid-token callback, so a client polling with a revoked token **leaks a full module graph per rebuild cycle**. **Fix:** call `Shutdown()`/unsubscribe on the evicted session's modules.

## M5 — directstream / debrid robustness

- **M5a — `serveHandoff` dead-warm hole: partial cached span < 1 MiB → empty-206 reconnect loop** · CONFIRMED. The 302-to-CDN fallback only fires when `CachedSpanFrom(ra.Start)==0` (`httpstream_warm.go:77`). A mid-body warm failure leaves `0 < span < 1 MiB`, so the serve loop's `IsRangeAvailable` fails, `warmFailed` returns after 206 headers with **zero payload**, and the player reconnects at the same offset forever. MpvCore direct-handoff only; user retry usually recovers. **Fix:** redirect to CDN whenever `warmFailed`, regardless of partial span.
- **M5b — `fetchRangeBytes` has no timeout while holding a per-token CDN gate slot** · CONFIRMED. `capturePrewarmWindows` holds a `tryAcquire`'d slot of the 2-cap per-token gate (`manager.go:315`) across an up-to-48 MiB GET with no request ctx and no `Client.Timeout` (`httpstream.go:54-67`); a stalled-but-alive CDN body blocks `io.ReadAll` forever, permanently consuming 1 of the link's 2 slots. After TTL a re-prewarm can hang the second slot → the serve path blocks at `acquire` and **the episode won't start**. The metadata parse on this same path *is* bounded (`metadataParseTimeout`); this read isn't. **Fix:** give `fetchRangeBytes` a bounded context.
- **M5c — Background preload can poll TorBox forever, stalling the serialized prewarm drain** · CONFIRMED. A `requestdl` failure on a *ready* torrent returns `("",false,nil)` and retries every 4 s indefinitely (`torbox.go:492-505`, `errRetries` cap only guards `getTorrent`). `preloadCtx` has no timeout (`stream.go:1217`); the drain runs targets synchronously (`prewarm.go:28-45`), so one stuck target leaks the drain goroutine, skips the tick's remaining targets, and consumes the shared `requestdlLimiter` against real plays. **Fix:** bounded `preloadCtx` + retry cap on the ready-but-failing branch.

## M6 — Playback UX / continuity

- **M6a — Loading screen reads a shadow atom nothing writes; pill stacks over it** · CONFIRMED. `VideoCoreLoadingScreen` reads `__debridstream_stateAtom` from `debrid-stream-overlay.tsx`, whose only writer (`DebridStreamOverlay`) the v3.9.0 merge unmounted (zero importers at HEAD). The mounted `PlaybackPlayPill` uses a **different same-named** atom and never checks `vc_loadingScreenVisibleAtom`, so the `z-[1000]` pill stacks over the loading screen during cold debrid start — the exact duplication `a8d1104d` claimed to fix. ~200 lines of dead `DebridStreamOverlay` remain. **Fix:** in `PlaybackPlayPill`, share one atom + hide the floating pill while the loading screen is visible; delete the dead overlay.
- **M6b — MpvCore player's OP/ED chapter detection wasn't updated with the Intro/Outro + duration fix** · CONFIRMED. `19bed7eb` fixed `vc_getOPEDChapters` (VideoCore) but `MpvCorePlayerContent` computes its own `opEdChapters` matching only `Opening`/`Ending` (`mpv-core-player-inner.tsx:583-619`), no Intro/Outro, no duration heuristic. Intro/Outro-labeled or unlabeled-chapter releases without aniskip data auto-skip in the browser but **not in Denshi MpvCore**. **Fix:** reuse `vc_getOPEDChapters`.
- **M6c — Durable `_lw` store rewrites its bucket 3–4× and re-decodes all items on every 1 Hz progress tick** · CONFIRMED. `setLastWatched` runs inside `UpdateWatchHistoryItem`, fired per StatusEvent with no throttle; web + MpvCore players emit `video-status` every 1000 ms. Each tick: `filecache.Set` full-rewrite, then `trimLastWatchedItems`→`GetAll` marshals+unmarshals every item and rewrites **twice more**, under `m.mu`, per active session, on the Pi (cap 1000 vs the resume store's 100; trim runs on every upsert). **Fix:** trim only on new-key insert; throttle the `_lw` mirror to ~10 s.
- **M6d — Denshi install prompt suppressed exactly when `disableUpdateCheck` is set** · CONFIRMED. `a66bc395`'s new effect early-returns on `disableUpdateCheck` before the `isDownloaded` modal open (`electron-update-modal.tsx:148-153`), but `main.js:1789` runs `checkForUpdatesAndNotify()` unconditionally with `autoDownload=true` — so the download completes silently with no Install button, the exact case the commit meant to fix. `autoInstallOnAppQuit=true` softens it → medium. **Fix:** don't gate the download-complete modal on `disableUpdateCheck`.

## M7 — Multi-user cache / authz

- **M7a — watch-room `join-stream` endpoint has no membership or password check** · CONFIRMED (authz bypass). `HandleNakamaWatchRoomJoinStream` (`nakama_rooms.go:211-289`, `5e8bc095`) gates only on `guardStreamingUser`, never checks `Participants` membership or the room password (unlike `JoinRoom` at `watch_room.go:345`). Any authenticated user who discovers a roomId (broadcast via `HandleNakamaWatchRoomList`) can start a stream piggybacking the controller's resolved release. Medium not critical: roomIds already broadcast title/media/HasPassword, the bypass never joins Participants, each peer resolves its own link. **Fix:** require Participants membership (or the room password) on join-stream.
- **M7b — Schedule/missing/upcoming caches are admin-only; non-admins recompute the schedule from AniList every request** · CONFIRMED (scope narrowed). Three handlers read/write their package caches `if sess.IsAdmin` (`526843bb`). The **schedule** path makes 1–2 uncached AniList GraphQL calls per non-admin request; missing/upcoming pay only CPU (collection + metadata come from per-user in-memory caches). Deliberate (avoid cross-user leak) but the schedule could be per-user-keyed like `tagsCache`/`statsCache` were.

---

# LOW

- **L1 — `useGetFranchiseRefs` query key is (count, min-id, max-id), not the id set** · CONFIRMED. `anime_franchise.hooks.ts:45` — two id sets sharing count+min+max hit the same 30-min-stale cache entry (fixed-page-size lists). Refs are per-`mediaId`, so a collision only yields missing grouping coverage (self-healing), never a wrong match → presentation-only.
- **L2 — Global `MaxConnsPerHost: 8` on the shared CDN proxy client** · CONFIRMED (impact minor). `httpstream.go:62` — >8 in-flight proxy-mode range requests to one CDN host queue cross-user. Mitigated by the later per-link cdngate (cap 2) and direct-CDN mode moving most traffic off this transport; needs 5+ concurrent proxy-mode streams to bite.
- **L3 — TorBox `fileIdCache` sync.Map has no eviction/TTL/cap** · CONFIRMED (impact minor). `torbox.go:43` — unbounded for process life (sibling `infoCache` got TTL+cap+flush, this didn't). Entries are tiny strings, keys never go stale-wrong → single-digit MB/year. **Fix:** give it the same cap as `infoCache`.
- **L4 — Prewarm cleanup replaced an indexed DELETE with full-table fetch + Go filter** · CONFIRMED (impact negligible). `800f59bd`'s `CleanupWatchedPrewarms` now `ListDebridPrewarms()` (no WHERE) then filters in Go, for per-row UserTags refcounting. Table is self-bounded to tens of rows, ticks every 10 min. Only the SELECT scope could be narrowed.

---

# Design-decision conflict — needs your call

## F-INV — Cached status dominates format quality (resolution/codec/REMUX) within an audio band
`70f34f49` replaced upstream's "70%-of-top-score floor" with an **unconditional cache-first sort inside a band**. `smartCachedPrioritization` sorts band (episode/season/audio) → cached → format score, so episode-relevance and language dominate cache, but **within a band any cached release beats every uncached one regardless of resolution** — pinned by the test *"Cached 480p beats uncached 1080p (same audio tier)"* (`autoselect_test.go:447-456`). `Rank()` backs both auto-select and the manual "Auto" sort (`finder.go:220`). The doc comment still claims "Cache is the outermost sort key," which no longer matches the band-first code.

Your standing invariant (memory `stream-selection-quality-over-speed`): *"cached-first must be a tie-break, never override quality."* Whether this **violates** it depends on whether resolution/codec counts as "quality" under your decree or whether "quality" means only the episode/season/audio band. Documented, test-pinned design choice — flagged for you to adjudicate, not auto-fixed.

---

# Refuted / dropped

- **cancelStream "clear the share triple" GetProvider early-return gap** · REFUTED. The early-return (`stream.go:1030`) leaves `streamUrl="" + stale ids`, but both readers (`GetUserStreamShare`, `RefreshStreamUrl`) return "no active stream" when `streamUrl` is empty, and every start path overwrites the ids before setting a URL. Unobservable; near-nil reachability (debrid unconfigured mid-stream). _(Not the same as M2c, which is a real distinct path.)_

[#866]: https://github.com/5rahim/seanime/issues/866
