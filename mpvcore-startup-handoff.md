# MpvCore startup latency — implementation handoff

Date: 2026-07-04. Follows the MpvCore/mpv-prism integration audit (this doc supersedes the
chat transcript; memory entries: `mpv-prism-startup-serialization`, `mpv-prism-overlay-constraint`,
`cdn-handoff-plan`).

## 0. Context / audit verdict (why this list exists)

- **No server-side regression** was found in "selecting torrent / adding torrent" — the
  mediacore/per-session commits (da8/57a/a8d/f84) don't touch those phases. The waits there
  are pre-existing (TorBox `AddTorrent` 500ms sleep, requestdl limiter, poll backoff).
  The wait *feels* longer because the debrid pill became a full-screen staged loading screen.
- The real multi-second cost is client-side, in three buckets:
  1. **Cold player/presenter creation** per watch session (fresh libmpv instance + GPU
     presenter attach; `load()` is serialized behind attach inside vendored `@mpv-prism`).
  2. **mpv opening the URL** — network first-byte + libavformat MKV probe (head **and tail**:
     SeekHead → Cues) + decoder init. Likely dominant; unmeasured.
  3. Post-`file-loaded` JS bookkeeping — **already fixed** (reveal gate, see §1).
- Hard constraint (bisect-proven, exitCode -36861): **never CSS-animate opacity of any layer
  over the live mpv canvas** — renderer crash. Instant swap only. See
  `mpv-prism-overlay-constraint` memory before touching the overlay.

## 1. Already done in working tree (UNCOMMITTED as of writing)

| Change | File | Status |
|---|---|---|
| Per-phase timing log (`mediaInfo/selection/addTorrent/urlResolve/fileCheck/total`) in one Info line at "Stream is ready" | `internal/debrid/client/stream.go` | done, tests pass |
| Reveal gate: only track-select + resume-seek gate the overlay swap; volume/speed/sub-style/shaders fire un-awaited; swap additionally requires `playback-restart` (real frames) with 3s force-reveal insurance | `seanime-web/.../mpv-core/mpv-core-player-inner.tsx` (`maybeRevealPlayback`) | done, typecheck OK |
| `hwdec=d3d11va` explicit on Windows (skips auto-safe probe; user config overrides) | same file, mpvOptions memo | done |
| Web-tab mis-target fix: `BeginOpen` downgrades MpvCore→VideoCore unless ws platform is `"denshi"` (`targetForClient`) | `internal/directstream/stream.go` + test + mock `ClientPlatforms` | done, tests pass |
| T11/T12 MpvCore checklist additions | `cdn-handoff.md` | done |

**Do not commit** `seanime-denshi/src/main.js` — it carries TEMP mpv-prism diagnostics
(console pipe + debug-video localStorage flag), marked as such in-file.

Build matrix: server changes → `scripts/deploy-server.sh`; player changes exist only in a Denshi
build (`scripts/build-denshi-local.sh`) — browser has no MpvCore, Denshi bundles its own web UI.

## 2. STEP 1 — Measure (do this before building anything below)

**2026-07-05: journal-based client timing shipped** — the client now reports a
`startup-timing` event at first playback-restart, logged as
`mpvcore: Client startup timing presentMs=… loadMs=… fileLoadedMs=… restartMs=…`
(all ms since the client received "watch"; -1 = mark missed). Instrumented marks:
presenter attach ready (`awaitPresentationReady`, the v1-residual attach cost),
`player.load()` resolved, mpv `file-loaded`, `playback-restart`. No console pipe
needed anymore — one Denshi playback + journal grep gives the full split.

1. Read: `debridstream: Stream is ready` line → per-phase server split;
   `Client startup timing` line → attach vs URL-open vs decode split.
2. Decision table:
   - `addTorrent` consistently ≥ ~500ms → §5 (trim TorBox sleep).
   - `presentMs` ≥ ~1s → §4 v2 (persistent video host).
   - `loadMs - presentMs` dominant → network/header path (§3 already shipped; residual
     is client downlink pulling the ~20MiB MKV header+fonts — not reducible).

**MEASURED 2026-07-05 (three preloaded plays, warm player v1 active):**
`presentMs` 525/819/674 — attach cost real but **below the 1s bar for v2**;
`loadMs` ≈ presentMs+2 (prism load() returns at loadfile issue, not file-loaded);
`fileLoadedMs` 2389/3207/2607 → **mpv open + header pull ~1.9-2.4s = dominant**;
`restartMs - fileLoadedMs` ≈ 280-300ms decode/track floor. Total ~2.7-3.5s.
→ Action taken: prewarm-time window capture (below). v2 stays shelved unless
presentMs grows or the header path stops dominating after the capture lands.

**Prewarm window capture — shipped 2026-07-05:** the old `warmStreamStart` throwaway
read (CDN-edge warm, bytes discarded) became `capturePrewarmWindows`
(`internal/directstream/manager.go`): at metadata-prewarm time it downloads head
(≥24MiB, bitrate-scaled up to 48MiB) + tail 4MiB and keeps them in a URL-keyed RAM
cache (`prewarmWindowCache`, 1h TTL, evicted in DropStreamMetadata). Play-time
`warmRange` fills the FileStream cache from RAM (log: "Probe window filled from
prewarm capture") instead of re-pulling ~28MiB from TorBox, so the client's header
read is no longer capped at TorBox→VPS tee speed. Only helps plays that had a
prewarm (the binge/preloaded path — exactly where the 3s was). Expected: fileLoadedMs
drops toward pure client-downlink (~1-1.5s); verify via startup-timing lines.

## 3. Head/tail prewarm serving — IMPLEMENTED 2026-07-05 (modified form), deployed

Re-prioritized after live plays: mpv's "Starting video..." seconds are its MKV probe
(header + attachment fonts + tail Cues) over cold TorBox round trips — Tenji/VideoCore
never pay this client-side. Shipped as:
- **MpvCore stays on the proxy even in direct-CDN mode** (`mpvCoreProxied`,
  `internal/directstream/httpstream_warm.go`; PlaybackURI branch in `loadPlaybackInfo`).
  VideoCore direct is untouched.
- **Probe windows warmed at stream bind**: head 24MiB + tail 4MiB pulled into the shared
  `FileStream` cache (token-gated, transient-retry), racing ahead of mpv's connect.
- **Stitched serving**: any cached prefix of a requested range is served from disk, the
  remainder continues as a live CDN tee filling the same cache
  (`serveStitched`, `FileStream.CachedSpanFrom`).
- **302 CDN handoff shipped 2026-07-05 (same day)**: in direct mode the proxy serves ONLY
  the warm windows (blocking on the in-flight warm) and 302s every other range to the raw
  CDN. The main sequential read is moved over by capping the head-window response at the
  window edge → mpv's reconnect re-requests at that offset → 302 → **ffmpeg adopts the
  redirect target**, so all subsequent reads/seeks go straight to the CDN. Direct-CDN
  egress restored for MpvCore (server serves ≤28MiB/stream). Guards: dead-warm +
  empty-cache → immediate redirect (no reconnect loop); failed cache-init marks both
  windows dead; small files never truncate or redirect. `TestHandoffWindow` covers the
  window boundaries.
- **NEEDS ONE LIVE PLAYBACK to validate** the truncate→reconnect→302 chain end-to-end
  (ffmpeg reconnect + redirect adoption verified by mechanism, not yet by a real Denshi
  session). If a playback ever stalls ~24MiB in: turn Settings → Debrid → Direct CDN
  playback OFF → full-proxy fallback (yesterday's stitched path) with no redeploy.
- Verification: `mpvcore: Client reported loaded-metadata` / `can-play` debug logs now
  give the journal timeline between "Signaling player that stream is ready" and frames.

Original Tier 1/2 design kept below for reference.
### (original design notes)

Idea: the preload/prewarm layer already resolves the CDN link minutes before play; extend it
so the **server serves the file's head+tail from a warm cache at open time**, so mpv's probe
never waits on TorBox first-byte. The "switch to CDN afterwards" happens at the HTTP layer —
never at the player layer (mpv URL switching = full reopen with visible stall; EDL stitching
rejected too).

### Tier 1 — proxy mode (server-only, small)
- At preload (`internal/debrid/client` preload path), after the CDN URL resolves: pull
  **first ~8–16 MiB + last ~2–4 MiB** into the directstream cache
  (`httpBaseStream`/`httputil.FileStream`, `internal/directstream/httpstream.go`; there is
  existing read-ahead bookkeeping in `httpstream_readahead.go` — reuse, don't duplicate).
- MKV probe reads the TAIL (SeekHead → Cues) — head-only prewarm leaves a cold CDN
  round-trip mid-probe. Cache both ends.
- Pace the prewarm fetch like the subtitle walk (6 MiB/s pattern, see
  `directSubtitleWalkBytesPerSec` in httpstream.go) — it shares the file's single TorBox link
  (v3.8.16 lesson), though at preload time nothing is playing.
- Add a per-range cache hit/miss debug log at stream open so Tier 1 can be verified live
  (goal: mpv's entire probe served from cache).
- No client change; benefits VideoCore and MpvCore proxy sessions equally.

### Tier 2 — direct-CDN mode with 302 handoff (moderate, after Tier 1 verifies)
- In direct mode, hand **MpvCore** the proxy URL instead of the raw CDN URL
  (per-target branch where `PlaybackInfo.PlaybackURI` is chosen,
  `internal/directstream/httpstream.go` ~`loadPlaybackInfo`; VideoCore direct stays as-is).
- Proxy handler: range within cached head/tail → serve from cache; anything else →
  **302 redirect to the raw CDN URL**. ffmpeg/mpv follows redirects per-request
  transparently — hot start + CDN egress (~zero VPS data-plane bytes after the head).
- Free bonuses: redirect target minted per-request → server can re-resolve an expired
  TorBox link mid-play; server regains a force-proxy control point (Chromecast caveat in
  cdn-handoff.md T11).
- Constraint check: does NOT reorder/filter torrent candidates — stream-selection
  quality-over-speed invariant untouched.

## 4. Warm player — v1 IMPLEMENTED 2026-07-05 (uncommitted); v2 designed

**Evidence update (2026-07-05):** Tenji (raw CDN → external player) and VideoCore+prewarm
both open near-instantly → the network open is NOT the multi-second bucket; §3's prewarm
tiers are DEPRIORITIZED. Also: `active=true` flips at `open-and-await` (mpv-core.tsx), so on
cold (non-preloaded) starts the player creation already overlaps the server's
selecting/adding seconds. The visible multi-second "Starting video..." hits exactly the
**preloaded** plays, where server time ≈ 0 and the client cold chain is all that's left.
Hence warm player = the right fix, promoted above §3.

**v1 (done):**
- `mpv-core.tsx` `MpvCore`: keeps `MpvCorePlayerInner` mounted for the app's lifetime when
  `__isElectronDesktop__ && window.electron?.mpvCore && settings.mediaPlayer.mpvPrismEnabled`
  → native libmpv instance (`bridge.create`) is warm from app launch; sessions no longer pay
  player creation. Config/deband changes remount via `key={warmEpoch}` **only while idle**
  (they're baked in at creation; same next-session semantics as before).
- `mpv-core-player-inner.tsx`: deactivation safety effect — on `active→false` the still-
  mounted player gets `sessionToken++`, `suppressEnd`, `player.stop()` (play-pill/overlay
  paths set `active=false` directly and used to rely on unmount for teardown).
- Web tabs / prism-disabled Denshi: `keepWarm` false → exact old behavior.

**v1 residual:** the GPU presenter attach is NOT warm — `MpvPrismVideo` lives inside the
vaul `MediaCoreDrawer` (`open={state.active}`), and vaul unmounts children when closed, so
attach (attachOffscreen + MediaStreamTrackGenerator + `video.play()` race, ≤~600ms worst
case on win32) re-runs per session and sits on the preloaded-play path.

**v2 (only if measurements show attach is still felt):** persistent video host — render
`MpvPrismVideo` into an always-mounted fixed container OUTSIDE the drawer (portal whose
target swaps between an off-screen host and the drawer body; React portal re-parenting
moves the DOM node without remounting, and MediaStreamTrackGenerator srcObject survives DOM
moves). Restructures the overlay children of `MpvPrismVideo` — real surgery on the
2900-line component; do not attempt without a measured need. Alternatives rejected:
vaul `forceMount` (closed-state content still painted; styling maze).

Upstream ask remains recorded (memory `mpv-prism-startup-serialization`): let `load()` run
during presenter attach; retry attach without recreating the player. If prism updates,
re-check both the residual and v2.

## 5. TorBox `AddTorrent` 500ms sleep — only if timing data shows it matters

`internal/debrid/torbox/torbox.go:391` — flat `time.Sleep(500ms)` on every genuinely-new add
("rate limiting" comment; pre-existing, deliberate rate-safety). If `addTorrent` phase logs
confirm it's a felt cost: halve it or make it conditional on recent API activity. Do NOT
remove outright without checking the rate-safety history (`c285a4fb` and the
`mylistCacheTTL` notes at torbox.go:723-751).

## 6. Validation leftovers (tracked in cdn-handoff.md, not new work)

- T11 MpvCore additions: MpvCore-direct subs/seek; thumbnail `?thumbnail=true` reads share
  the single TorBox link while scrubbing; web tab under MpvPrism-enabled session must get
  VideoCore signaling (the §1 downgrade fix) and still play.
- T12: measure MpvCore **proxy** egress separately — mpv's demuxer readahead is far more
  aggressive than an HTML5 `<video>`; proxied MpvCore may pull materially more VPS bytes.
  (Tier 2 in §3 changes this calculus — re-measure after.)

## 7. Explicitly deferred / rejected

- Player-level URL switching & EDL stitching for §3 — rejected (visible reopen/hitch).
- Keep-warm via hidden `display:none` video or opacity tricks — rejected (renderer crash).
- Any candidate reordering by cached/instant status — forbidden by standing invariant.
