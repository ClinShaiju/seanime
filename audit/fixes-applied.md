# Fixes applied — 2026-07-10

Follow-up to the audit in this directory. All changes are on `main`, unstaged. Full server build (`go build ./...`) is green, `go vet` is clean on touched files, web typecheck (`scripts/check.sh web`) passes, and `internal/torrents/autoselect` tests pass (including new ones).

## Fixed and validated

### Critical
- **C1/C2/C3 — auth boundary (root cause).** `isTrustedRequest` returned `true` for any password holder, so every "privileged" guard was a no-op on a password-protected multi-user server. Fixed at the guard layer: `guardPrivilegedLocalExecution` and `guardPrivilegedExtensionManagement` now require **admin identity** when a server password is set (`local_security.go`) — this closes extension-install RCE (C1) and server self-update (C2) for non-admins/anon in one place. Secret redaction (C3): added `redactSettingsSecretsForNonAdmin` and applied it in `NewStatus`, `HandleGetSettings`, and `HandleGetDebridSettings` so torrent-client/VLC/translate/nakama/debrid credentials are blanked for non-admins.
- **C4 — server-crash panic.** Nil-guarded `playbackCtx` at the entry of `StartSubtitleStreamP` + `recover` on the detached subtitle goroutine (`subtitles.go`, `httpstream.go`); also fixed the identical unguarded `WithCancel(nil)` in `localfile.go`. Added a locked `Manager.PlaybackCtx()` getter and routed the previously-unsynchronized reads through it (race fix).

### High
- **H2 (#866) — browser codec strings.** HEVC codec string was hex-level + literal `O`; now decimal level + `.B0`. AV1 level fixed hex→decimal too (`mediastream/videofile/info.go`).
- **H1 — watch-room sync dead on MpvCore: NOT fixed (deferred).** See below.

### Medium
- **M1a** batch-vs-single size tie-break (`sizeTieBreak`), **M1b** `RequireLanguage` now consults flag-emoji languages + always runs the name fallback, **F-INV** cached-first now only wins **within a resolution tier** (quality floor, `resolutionTier`) — your chosen resolution. Tests updated + 2 new unit tests.
- **M1c** auto-select status now routes per-user (`evFor`) and is silenced for background preloads (`WithStatusRouting`).
- **M2a** `GetStreamURL` is deterministic (lowest active userID). **M2b** per-user PlaybackManager subscription key. **M2c** `cancelStream` nil-prevOpts resolves via `opts.UserID` instead of the admin manager.
- **M3a** debrid auto-resume now arms for MpvCore (hook is player-aware + mounted in `MpvCore()`).
- **M4a** session eviction now tears down per-user videoCore/mpvCore/mediaCoord/directStream (added `Shutdown()`/unsubscribe so listener goroutines don't leak on relink/logout).
- **M5a** serveHandoff redirects to CDN on any warm-fail (kills the empty-206 loop). **M5b** bounded `fetchRangeBytes`. **M5c** capped TorBox requestdl retries + bounded `preloadCtx`.
- **M6a** loading screen reads the live atom + pill hides while the loading screen is up. **M6b** MpvCore reuses `vc_getOPEDChapters`. **M6c** durable `_lw` mirror throttled to 10 s on the progress path (explicit reopens still immediate). **M6d** Denshi install prompt no longer suppressed by `disableUpdateCheck`.
- **M7a** watch-room join-stream now requires room membership (`IsParticipant`). **M7b** schedule/missing/upcoming caches are per-user keyed.

### Low
- **L1** franchise query key uses the full id set. **L2** shared CDN `MaxConnsPerHost` 8→16. **L3** TorBox `fileIdCache` bounded (mutex+map, cap 4096).
- **L4** (prewarm full-table cleanup) — intentionally skipped: negligible (self-bounded table, 10-min tick) and re-adding the indexed DELETE conflicts with the per-row refcount design.

### Bonus (pre-existing break found during validation)
- `internal/debrid/client` test package didn't compile — `models.NewDefaultDummyDebridSettings` was referenced but never defined (dummy-debrid merge). Added the constructor.

## Done but needs live testing — H1 (watch-room sync on MpvCore)

**H1** was fixed by introducing a player-agnostic `PlayerSyncControl` interface (`video-core-atoms.ts`) that both VideoCore's DOM bridge and MpvCore's native player populate. The sync hook (`nakama-room-sync.ts`) now reads this interface instead of a raw HTMLVideoElement, so MpvCore (Denshi's default player since v3.9) is fully wired:

- **Emit (driver):** MpvCore populates `currentTime`/`paused`/`duration`/`seeking`/`readyState`/`playbackRate` from its atom refs; VideoCore wraps the DOM element natively. Both write the same `vc_globalPlayerSyncControl` atom.
- **Apply (follower):** `.play()`/`.pause()`/`.seek(t)`/`.setPlaybackRate(r)` dispatch to MpvCore's `setPaused`/`seek`/`setSpeed` IPC calls (or VideoCore's DOM methods).
- **Discrete events:** play/pause/seeked DOM listeners are attached only when a `domElement` is available (VideoCore); MpvCore relies on the heartbeat (1-2s resync, not instant — acceptable, matches the server-authoritative heartbeat interval already used).

The sync hook also now observes `mpvCore_stateAtom` for `playerActive`/`playbackInfo` (not just `nativePlayer_stateAtom`), and `streamType` routing is derived from whichever player is active.

**Not yet live-tested** — this needs a Denshi build + two accounts in a real room. The change type-checks, the interface is narrow and well-defined, and the sync timing logic is unchanged (only the player abstraction layer changed), so regression risk is low.

## Latent, not fixed — M3b (nakama Manager binds admin Coordinator)

**M3b** is about the **upstream** watch-party server-side paths (`listenToPlaybackAsHost`, `WatchPartyGenericPlayer`) using the admin's Coordinator. The fork's room model doesn't use these paths — it uses client-side WebSocket relay via `useWatchRoomPlayerSync`. Since the upstream watch-party UI was replaced by the rooms model, M3b is effectively dead code on this deployment. Left unfixed.

## Test note
Native-Windows `go test` fails several `internal/continuity` and `internal/debrid/client` tests, but **only** on environment issues: sqlite `.db` file-lock at `t.TempDir` cleanup, and `.tmp` hidden-dir/zip-extract/file-move behavior in the download path (code untouched here). No logic assertion fails. The project runs tests on Linux via `scripts/` where these don't occur.
