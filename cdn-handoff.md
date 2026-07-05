# CDN Handoff — direct video to clients, subtitles stay server-side

**Status: MERGED to main + DEPLOYED to VPS as v3.8.14 (2026-07-02); GitHub release
v3.8.14 published on the fork. T8 shipped in Denshi 3.8.15 (release v3.8.15,
2026-07-02) — the Direct CDN playback toggle is now usable from Denshi 3.8.15+
against the VPS (server side was already in 3.8.14; the 3.8.15 server binaries are
identical apart from the version string, no VPS redeploy needed).**
Server (T1–T6b) + web client (T7 resilience, T9 capability flag) + Denshi CORS (T8)
done, unit tests added (T10 code part), build/vet/tests green.
Tenji needs nothing: its debrid playback is `playbackType: "externalPlayerLink"`
(raw CDN into mpv, no CORS constraints) — direct mode only affects `nativeplayer`.
**First live session (2026-07-02, v3.8.15) findings → fixed in v3.8.16:**
- **The dual-link premise is void on TorBox**: `requestdl` is idempotent per (torrent, file)
  — both resolves returned byte-identical URLs, so client video and server subtitle reads
  always shared ONE link. The duplicate resolve is removed (one `requestdl` per stream
  start); instead the server behaves on the shared link: the subtitle cluster walk is paced
  (6 MiB/s, was line speed — the cause of mid-episode 429s → video errors → buffering), and
  a walk that still dies on a sustained throttle retries after a cooldown, resuming from the
  last delivered cluster. Note this also voids the "N room members, N links" premise —
  same-account room members share the file's one link.
- The "Tenji won't start playback" seen that session was NOT direct-CDN related: the server
  restart rotated the per-boot client-identity secret, orphaning Tenji's websocket from its
  client id, so `external-player-open-url` went nowhere. Fixed: secret persisted
  (`server_values` table), proof TTL 30d, and Tenji reconnects its socket when its identity
  diverges from the socket's registration.

Remaining:
- **T11/T12**: live VPS validation + egress measurement (Denshi direct + web
  tab proxied + two-account watch room).
- Cast caveat accepted for now: video-core-cast would hand the raw CDN URL to Chromecast in
  direct mode — verify or force-proxy during T11.

## Goal

Native-player (Denshi/web) debrid playback pulls video **directly from the debrid CDN**
instead of proxying every byte through the VPS. The server keeps doing what clients
can't: MKV metadata parse, ASS subtitle extraction + push, font serving. Tenji already
plays raw CDN via its mpv module (`externalPlayerLink` flow) — this brings the same
topology to the native player without losing the subtitle pipeline.

Wins: VPS egress ≈ 0 for playback (the metered direction on Oracle), user↔VPS hop
removed from the video path (the watch-room buffering / seek-thrash hop), TorBox
per-link pressure split across two links (client video link + server parse link).

## Current state (verified 2026-07-01)

- Only `PlaybackTypeNativePlayer` proxies. External player + MPV paths already hand
  off the raw CDN URL (`internal/debrid/client/stream.go:815-855`).
- The proxy (`internal/directstream/httpstream.go` `getStreamHandler`) does 4 jobs:
  serve bytes, learn playback position from Range offsets (drives subtitle streams),
  fill the shared `FileStream` cache (subtitle readers read from it), 429/backoff +
  per-token gate (`cdngate.go`).
- **Position-driven subtitle sync already exists**: `VideoSeekedEvent` →
  `startSubtitleStreamForTime` (`stream.go:510-512`) → `subtitleOffsetForTime`
  linear time→byte estimate (`subtitles.go:172`, tested). No Cues parsing needed.
- **Metadata parse is already CDN-direct**: `fetchMetadataReader` uses
  `NewChunkedHttpReadSeeker` (`httpstream.go:139`), independent of the proxy.
- Watch-room join already resolves a **separate CDN link per consumer** from a shared
  `torrentItemId` (`internal/debrid/client/stream.go:48,110`) — the pattern task 4 reuses.
- Fonts/attachments served from the server parse (`serve.go`) — unaffected.

## Design

Per-stream opt-in "direct mode" for `DebridStream` only (local/torrent/URL/Nakama
unchanged). In direct mode:

- `PlaybackInfo.StreamUrl` = raw CDN URL (client-facing link).
- Server resolves a **second** link from the same torrentItemId/fileId for its own
  metadata + subtitle readers, so client and server never contend on one link.
- Subtitle streams are driven purely by player events: initial stream at offset 0 on
  video-loaded, refresh on `VideoSeekedEvent` (existing path). Subtitle readers use
  chunked CDN readers instead of `FileStream` readers.
- The proxy endpoint stays fully intact as automatic fallback: web tabs (CORS),
  Nakama relay/watch-party, thumbnails, and any client that doesn't opt in.

Eligibility gate: client declares support (Denshi capability flag or settings toggle)
AND stream is debrid-sourced — **including watch-room participants**. Each room
participant's stream resolves its **own client CDN link** from the shared
torrentItemId/fileId (the join path already shares the selection this way), so N
room members = N independent CDN links, zero shared-link contention, no host relay.
Proxy fallback only for non-capable clients (web tab) and true cross-server Nakama
relay. Otherwise → proxy, byte-for-byte today's behavior.

Non-goals: torrentstream/localfile direct mode, per-user debrid accounts, removing
the proxy, web-tab CORS workarounds (web tab keeps proxying).

## Risks / open questions

- **Client `<video>` dies on CDN 403 (expired link) / 429** where the proxy retried.
  Mitigated by task 7 (re-resolve + resume). This is the largest chunk and lives in
  Denshi/seanime-web, not the server.
- **CORS**: debrid CDNs send no CORS headers. Denshi injects headers via
  `onHeadersReceived` in main process (its own fork). Web tab = proxy fallback, no work.
- **Server ingress remains** ~file-size per stream (subtitle cluster walk). Free on
  Oracle (ingress unmetered) but still NIC traffic. Acceptable; revisit only if the
  NIC saturates.
- **TorBox multi-IP visibility**: client IPs now hit the CDN directly on a shared
  account. Tenji has done this for months without issue; provider goodwill, not
  guaranteed. RealDebrid would NOT tolerate this (IP-locked links) — direct mode must
  stay per-provider gated (TorBox allowlist first).
- **Subtitle timing after seek** uses the linear estimate — same accuracy as today's
  seek path, so no regression, but direct mode loses the exact Range-offset signal.
  If subs lag after seeks in practice, add MKV Cues parsing to mkvparser (deferred).
- **Prewarmed parser reuse** (`parserCache`) keys on stream URL; with two links per
  stream, make sure the cache key is the *server* link (the one the parser reads).

## Task list

Phases ship independently; 1-4 are server-only, deployable behind the flag with zero
behavior change until a client opts in.

### Phase 1 — server: direct mode plumbing
- [x] **T1. Direct-mode flag.** Add `DirectCdnPlayback` (settings or per-request
  capability flag from client) + provider allowlist (TorBox only initially). Plumb
  into `PlayDebridStreamOptions` / `DebridStream`.
- [x] **T2. Dual-link resolve.** In `debrid_client.startStream` (native-player case,
  direct-eligible), resolve a second stream URL from the same torrentItemId/fileId
  (reuse the watch-room share pattern). Client link → `PlaybackInfo.StreamUrl`;
  server link → metadata/subtitle readers. Fall back to single link if re-resolve
  fails (both use it — still works, just contends).
- [x] **T3. `loadPlaybackInfo` direct branch.** When direct: `StreamUrl` = client CDN
  link (no `{{SERVER_URL}}` template, no HMAC param), keep `ContentLength` +
  metadata parse exactly as-is (already CDN-backed). Key `parserCache` on the server
  link.
- [x] **T4. Subtitle readers off `FileStream`.** In direct mode, `DebridStream`
  subtitle readers (`startSubtitleStreamForTime` case + initial stream) use
  `NewChunkedHttpReadSeeker(serverLink)` instead of `s.getReader()`. Initial
  subtitle stream (offset 0) kicks on the player's loaded/first-status event instead
  of the first proxied Range request.

### Phase 2 — server: lifecycle & safety
- [x] **T5. Link refresh endpoint.** Endpoint/ws-request: "my stream link died,
  re-resolve" → fresh client link from stored torrentItemId/fileId (machinery exists
  in preload URL refresh). Return new URL; client swaps src and seeks back.
- [x] **T6. Fallback correctness.** Verify proxy path is untouched when flag off /
  client not capable / provider not allowlisted. Thumbnails and true cross-server
  Nakama relay keep using the proxy endpoint even for direct-mode streams (endpoint
  stays alive per stream).
- [x] **T6b. Watch-room per-participant links.** Room playback: every participant's
  DebridStream resolves its own client CDN link from the shared torrentItemId/fileId
  (extend the existing `SharedTorrentItemId` join path through T2's dual-link
  resolve). Host and followers each get direct video; sync stays event-driven as
  today. Mixed rooms work: a non-capable participant falls back to proxy without
  affecting the others.

### Phase 3 — Denshi client (H:/Projects/denshi or seanime-web nativeplayer)
- [x] **T7. CDN resilience.** On `<video>` network/HTTP error mid-playback: call T5,
  swap `src`, restore `currentTime`, resume. Cap retries; surface toast on give-up.
  Apply the seek-reset lessons (cooldown, no thrash).
- [x] **T8. CORS injection.** Electron main process `onHeadersReceived` → adds
  `Access-Control-Allow-Origin: *` to **media-resourceType responses that lack the
  header** (never overrides an origin-set header). Scoped by resource type rather
  than a CDN hostname allowlist — TorBox CDN hostnames vary and the AIOStreams
  single-link fallback can be any host; media-only injection in our own windows is
  the tighter invariant. Shipped in Denshi 3.8.15 (`seanime-denshi/src/main.js`,
  508c9954).
- [x] **T9. Capability handshake.** Denshi advertises direct-CDN support (client id /
  header / settings), server uses it for T1 eligibility. Web tab advertises nothing
  → proxy.

### Phase 4 — validate & ship
- [x] **T10. Unit tests.** Direct-branch `loadPlaybackInfo` (URL selection, cache
  key), dual-link fallback, subtitle-reader selection. Extend existing
  `subtitles_test.go` / `httpstream_test.go` patterns.
- [ ] **T11. End-to-end on VPS.** One Denshi client direct + one web tab proxied on
  the same episode: video plays, ASS subs render, fonts load, seek refreshes subs,
  episode-end tracking fires. Then a two-user watch-room (distinct accounts — same
  account rooms are known-broken): both direct, each on its own CDN link, sync
  holds through seeks. Watch server log for 429s on all links.
  **MpvCore additions (v3.9):** subs/fonts checks above are VideoCore-only — MpvCore
  demuxes subs itself (`shouldProcessMediaOnServer()` false, no server subtitle walk).
  Add: (a) MpvCore-direct session — subs render, seek works, mpv pulls raw CDN URL
  (no CORS involved, libmpv fetch); (b) seekbar thumbnails during MpvCore-direct
  playback: the proxy's `?thumbnail=true` reads come off the server's copy of the
  SAME single TorBox link mpv plays from — watch for 429s while scrubbing;
  (c) web tab under a MpvPrism-enabled session gets VideoCore signaling
  (`targetForClient` downgrade, directstream/stream.go) — verify browser playback
  still works with the setting on.
- [ ] **T12. Measure.** Compare VPS egress + seek latency before/after (one episode
  each way). Confirm the win is real before making direct the Denshi default.
  **MpvCore additions:** MpvCore-direct should be near-zero server egress (no video,
  no subtitle walk — best case). But measure MpvCore **proxy** mode separately: mpv's
  default demuxer readahead is far more aggressive than an HTML5 `<video>`, so
  proxied MpvCore sessions may pull materially more bytes through the VPS than
  VideoCore did.

## Pre-implementation map (recon 2026-07-01, anchors verified at 33c0197a)

### T1 — flag + eligibility
- Settings model: `DebridSettings` at `internal/database/models/models.go:617-627`
  → add `DirectCdnPlayback bool` (gorm column + json). Web form:
  `seanime-web/src/app/(main)/settings/_containers/debrid-settings.tsx`; regen/hand-sync
  `seanime-web/src/api/generated/types.ts`.
- Provider allowlist: gate on `DebridSettings.Provider == "torbox"` (helper in
  `internal/debrid/client`).
- **Capability signal (decision needed at impl time):** recommend an explicit
  `directCdnCapable bool` field on the `/debrid/stream/start` body (set by the web
  client only when running inside Denshi/Electron), NOT user-agent sniffing. Flows
  through `StartStreamOptions` (`internal/debrid/client/stream.go:98`) which already
  carries `UserAgent`/`ClientId`. Web tab sends false → proxy.

### T2 — dual-link resolve
- Resolve site: `startStream` download goroutine, `internal/debrid/client/stream.go:577-640`
  (`provider.GetTorrentStreamUrl(ctx, debrid.StreamTorrentOptions{ID, FileId}, ch)` —
  interface at `internal/debrid/debrid/debrid.go:22,39`). After the primary URL
  resolves, one more `GetTorrentStreamUrl` call with the same item/file = client link.
- **Caveat found:** pre-resolved AIOStreams direct streams (`selectedTorrent.StreamUrl != ""`,
  stream.go:467-470) have NO `torrentItemId` — second link impossible. Fallback:
  client + server share the single link (still strictly better than today: consumers
  split, but same-link contention possible; the cdn gate only covers server readers).
- Plumb: add `ClientStreamUrl string` to `directstream.PlayDebridStreamOptions`
  (`internal/directstream/debridstream.go:44`) and a field on `DebridStream`/`httpBaseStream`.
  Server link stays in `streamUrl` (all existing readers keep working untouched).

### T3 — loadPlaybackInfo direct branch
- `internal/directstream/httpstream.go:236` — the `StreamUrl: "{{SERVER_URL}}/api/v1/directstream/stream?id=..."`
  line. Direct mode: emit `ClientStreamUrl` instead (no template, no HMAC param).
- `parserCache` get/set at httpstream.go:250 and :308 already key on `s.streamUrl`
  (server link) — correct as-is once client link is a separate field. Verify prewarm
  (`PrewarmStreamMetadata`) also keys on the server link.
- Web client passthrough confirmed safe: `video-core.tsx:1134` does
  `.replace("{{SERVER_URL}}", ...)` — no-op on a raw CDN URL. **Cast caveat:**
  `video-core-cast.tsx:163` would hand the raw CDN URL to Chromecast; either force
  proxy URL for cast or accept it (CDN is reachable; CORS untested). Flag in T11.

### T4 — subtitle readers + initial kick
- Reader swap: `DebridStream` case in `startSubtitleStreamForTime`
  (`internal/directstream/subtitles.go:231-237`) uses `s.getReader()` (FileStream).
  Direct mode: return a `httputil.NewChunkedHttpReadSeeker(serverLink, headers)`
  wrapped in the cdn gate (`gatedReadSeekCloser` + `cdnTokenGateInst.acquire`, pattern
  at `httpstream.go:139-151`) — cleanest as a `newSubtitleReader()` method on
  `httpBaseStream` that branches on mode.
- Initial kick: `VideoLoadedMetadataEvent` handler at `internal/directstream/stream.go:494-509`
  already starts offset-0 subtitle streams for LocalFile/Torrent — add the
  direct-mode http case there. Proxy mode keeps its range-triggered kick
  (`httpstream.go:369-381`), which simply never fires in direct mode.
- Seek refresh (`VideoSeekedEvent` → stream.go:510-512) works unchanged once the
  reader swap is in.

### T5 — refresh endpoint
- Routes: `internal/handlers/routes.go:580-582` (`/debrid/stream/start|cancel|prewarm-status`)
  → add `POST /debrid/stream/refresh-url`. Handler resolves a fresh client link from
  the active stream's stored state — accessor already exists:
  `GetStreamURL`-style triple at `internal/debrid/client/stream.go:194`
  (`currentStreamUrl, currentTorrentItemId, currentFileId` under `stateMu`).
  Per-user scoping via the same opts→session resolution the other handlers use.

### T6b — watch-rooms
- Peer path already resolves its own link from `SharedTorrentItemId`
  (`stream.go:471-475, 516-521`) — direct mode composes with it for free: the peer's
  own stream goes through T2's dual-link resolve like any other. Host likewise.
  Cross-server Nakama relay (`/nakama/host/debridstream/*`, routes.go:613-615) is
  untouched (proxy).

### Tests / verification anchors
- Extend `internal/directstream/subtitles_test.go` (offset-for-time tests exist) and
  `httpstream_test.go` / `httpstream_cdn_retry_test.go` patterns.
- After changes: `go build ./...` + `go vet` + `go test ./internal/directstream/... ./internal/debrid/...`
  with the portable SDK (`export GOROOT="$HOME/go-sdk/go"`).

### Client work (separate repos, after server phase)
- Denshi: CORS header injection in Electron main (`onHeadersReceived`), 403/429
  re-resolve+resume calling T5, capability flag (T9). Web repo hosts the videocore
  error-handling changes (`video-core.tsx` error path).
- Tenji: **no changes** — already raw-CDN via externalPlayerLink + expo-mpv-player.

## Deferred (not in scope)
- MKV Cues parsing for exact time→byte (only if linear estimate proves inaccurate).
- Direct mode for torrentstream (server-local data, no CDN) and URL streams.
- Per-user debrid accounts (removes the shared-IP concern structurally).
- Pacing the subtitle cluster walk to playback speed (today it runs at parse speed;
  fine unless ingress/link load becomes a problem).
