# Handoff — debrid autoselect / quality / AIOStreams session

Read **CLAUDE.md** (deploy runbook, VPS, Go SDK) and **`~/.claude/projects/H--Projects-seanime/memory/`**
(infra state, creds, AIOStreams topology) first — this doc assumes those. No secrets here.

---

## SESSION 2 UPDATE (2026-06-23) — Nakama rooms + CDN-429 stall fix

Everything below is **uncommitted working tree**, all build/vet/test/typecheck green.
**DEPLOYED 2026-06-23 ~20:53 UTC** (full chain: web rebuild + arm64 → VPS; service active, HTTP
200, `watch-room/list`→401 confirms new binary, clean startup). Companion docs: `nakama-room.md`
(rooms spec, all decisions), `tenji-parity.md` (Tenji port status), memory `nakama-rooms-pool`.

### Nakama same-instance watch rooms (#13) — BUILT, not yet live-tested
A **parallel** system to the existing peer/host watch party (doesn't touch it). Pool of local
users → multiple rooms → discovery cards → join → synced playback.
- **Backend** `internal/nakama/watch_room.go` (+ `nakama_rooms.go` handlers, routes, events):
  `WatchRoomHub` — pool identity (`PoolUser`, source-namespaced for external later), room
  registry, create/join/leave/list, sha256 password, host promotion (auto-promote by join
  order + original reclaims on reconnect), **multi-source control** (host grants per-member or
  everyone), **force-host-tracks** (default off), **autoskip vote** (on/off/auto, majority wins,
  x/x display). 9 unit tests. API: `/api/v1/nakama/watch-room/{list,create,join,leave,control,
  force-tracks,autoskip}`. WS-disconnect hook in `handlers/websocket.go` hands off control.
- **Sync** = relay control ACTIONS (play/pause/seek) gated by `resolveRelay` (host or granted);
  payload position-only (per-user tracks stay local). Events `events.NakamaRoom*`.
- **Web** `nakama-manager.tsx` `WatchRoomsSection` — discovery cards (cover/name/Host/show·ep/
  yellow lock/members/Join), inline create+password, in-room member list + host control panel +
  force-tracks switch + autoskip 3-way vote. Player sync `nakama-room-sync.ts` drives
  `vc_videoElement` (works for Denshi native player — it's a `VideoCore` HTMLVideoElement),
  emit/apply + 800ms echo guard. Autoskip gate in `video-core-time-range.tsx`: only the
  controller auto-skips (no desync). Codegen run → `NAKAMA_ROOMS` types/endpoints.
- **Tenji** (other session): rooms ported + tsc-clean (uncommitted, native build separate).
  See `tenji-parity.md` STATUS block. MPV sync detects seek by diffing onProgress vs wall-clock.
- **REMAINING**: live 2-client test (Denshi web/native ↔ Tenji MPV) — the whole point of the
  deploy. Tenji `Status_UserDebrid`↔`UserDebridStatus` drift untouched (§3, deferred).

### CDN-429 "Denshi stops playing mid-episode" — FIXED
Root cause: the directstream re-pulls byte-ranges from the TorBox CDN as you watch; a transient
**CDN 429** (delivery throttle, NOT the 60/hr create-torrent API) hit `httpstream.go:312` which
failed the whole stream on first try (no retry) → player saw 429 → surfaced as "media format
not supported". Fixes in `internal/directstream/httpstream.go`:
- **Retry/backoff** on 429/502/503/504, up to 4×, honors `Retry-After`, aborts if client
  disconnects, permanent 4xx still fail fast. Unit-tested (`httpstream_cdn_retry_test.go`).
- **`MaxConnsPerHost: 8`** on `videoProxyClient` — bounds concurrent CDN range bursts.
- (Other session) `internal/debrid/client/stream.go`: `urlRefreshTTL` 15m→2h (TorBox links last
  ~3h; play-time refresh only, small win). Jitter idea reverted — at single-user scale the
  background prewarm is ~3 createtorrent/hr (≤5% of cap); per-tick cap is the right lever if it
  ever scales. Confirmed the existing 24h-finished / 1h-releasing selection TTL is correct.

### #29 parallel batch+single autoselect search — DONE (committed earlier? no — uncommitted)
`internal/torrents/autoselect/search.go` fires batch + single concurrently. Same search count,
no extra aggregator load. Tested.

### Uncommitted tree is large — suggested commit grouping before/after deploy
CDN-429 fix · urlRefreshTTL · Nakama rooms backend · Nakama rooms web · #29 autoselect · theme
`userId` placeholder (codegen surfaced profile-work staleness). Tenji changes are a separate repo.

## State at handoff
- **Seanime**: committed `38dc857f` on `main`, pushed to **fork** (`ClinShaiju/seanime`). Prior tip was `54ed3a30`.
- **AIOStreams fork**: committed `d0c9ecac` on `main`, pushed to `ClinShaiju/AIOStreams`.
- All Seanime backend+web changes are **deployed to the VPS** (server active, HTTP 200). A new **Denshi installer** is built but the **user must install it** to get the web-UI badge changes (`seanime-denshi/dist/seanime-denshi-3.8.7_Windows_x64.exe`). Web-browser users need a **hard-refresh**.

---

## What shipped this session

### 1. Torbox dedup — concurrent "Adding torrent" stall (~11s → ~1s)
`internal/debrid/torbox/torbox.go`. The AddTorrent dedup pre-check fetched the whole ~6.9 MB mylist with `bypass_cache=true` (slow, spiky) on a 6s TTL, so back-to-back/concurrent plays each re-fetched it. Now: `getTorrents(bypassCache bool)`, dedup path uses `bypass_cache=false`, TTL `mylistCacheTTL = 120s`, and AddTorrent appends the just-added `{ID,Hash}` to the cache so the long TTL stays accurate. Public `GetTorrents()` still uses `true`. Regression test: `internal/debrid/client/cache_flags_cloudbolt_test.go` (verifies `[TB☁️⚡]` cloud+bolt = cached).

### 2. Quality-aware autoselect ranking
`internal/torrents/autoselect/comparison.go` `calculateScoreBreakdown`. Added **within-band bonuses** (never cross the ±2000 English-dub band, so English/dual is always picked when present):
- `scoreBitDepth10=12`, `scoreHDR=8`, `scoreLosslessAudio=10` (FLAC only — decodes in the native Chromium player; DTS-HD/TrueHD/PCM deliberately excluded), `scoreSeadexBest=30`.
- `scoreRemux=100` — REMUX is the top within-English preference (sized to beat all other bonuses combined, far below the band). Rule verified: English-REMUX > English-non-REMUX > JP-only-REMUX.
- **Resolution name-fallback**: resolution was the only signal trusting habari alone; it now also `containsBoundedTerm(name, res)` like codec/source. Fixes releases whose resolution habari can't parse (e.g. "SeaDex 1080p (Best)" → `res=""`) being buried 100 pts.
- Tests: `internal/torrents/autoselect/langflag_diag_test.go` (`TestLangFlag_SeaDexFormatterName`, `TestPriority_RemuxWithinEnglish`).

### 3. Flag-language tags + Original+Dub / Dubbed badges
Aggregator releases express languages only as flag emoji (🇬🇧🇯🇵); `CleanReleaseName` strips emoji before habari, so language was lost.
- Backend: `internal/util/release_name.go` `DisplayLanguagesFromFlags` + `MergeLanguages`; folded into metadata at both parse sites in `internal/torrents/torrent/search.go`. Test: `internal/util/flag_languages_test.go`.
- Web: `seanime-web/.../torrent-search/_components/torrent-item-badges.tsx` renders **Original + Dub** (JP + a dub lang) and **Dubbed** (dub-only, no JP) from the flag-derived languages.
- Note: scoring already decoded flags via `LanguagesFromFlags`/`audioLanguageScore`; this was display-only.

### 4. AIOStreams fork — size-strip parser guard
`packages/core/src/parser/file.ts` `FileParser.parse`: strip `\d+(\.\d+)?\s*[KMGT]i?B` size tokens from the string fed to parse-torrent-title, so "465 MB" stops being read as episode 465. `result.size` (the 📦 badge) is a separate numeric field — untouched. **Verified live**: EMBER pack now `episodes:None, seasonPack:True`.
- **Deployed by building the fork image ON the VPS** (`aiostreams-fork:latest`, 294 MB) and pointing the compose at it: `/home/stremio/aiostreams/compose.yaml` line 7 stock→fork. Build dir left at `~/AIOStreams-build`.
- **Maintenance**: aggregator is now a **self-built fork image**, not stock. To take upstream updates: `cd ~/AIOStreams-build && git pull && sudo docker build -t aiostreams-fork:latest . && cd /home/stremio/aiostreams && sudo docker compose up -d aiostreams`. Watchtower is already disabled for it.

### Earlier in the session (already deployed, pre-`38dc857f`)
- Diagnosed the "one client stuck on selecting torrent": **not** a Seanime lock (ruled out TorBox client, goja pool, fetch sem, AutoSelect, repo search). Root was the **AIOStreams aggregator** waiting on the slow **AnimeTosho** addon (~10s) + RD 429s. **User disabled RealDebrid + AnimeTosho** in the AIOStreams config → search ~10s → ~2s. See memory `aiostreams-topology-latency`.

---

## Resolved investigations (no code needed)
- **Audio-switch "doesn't work" on some titles** = single-audio (JP-only) releases — switching works fine when ≥2 audio tracks exist. Memory `directstream-audio-switch-gap`. Probe a debrid file: resolve the `nyaa.clinshaiju.dev/debrid/...` URL's redirect, then `docker exec jellyfin /usr/lib/jellyfin-ffmpeg/ffprobe ...`.
- **EMBER "Dual Audio" mislabel** = the nyaa.si torrent **title literally says "Dual Audio"** (nyaa.si/view/1418782, /1538016) — EMBER pack-level label; the specific ep004 file is JP-only AC3. Faithful parse, not a bug. No title parser can catch a pack-claims-dual-but-this-file-isn't mismatch.
- **Seeders show "No seeders"**: the pipe is complete (nyaa addon emits `👤 N` → AIOStreams `seedersRegex` → mapper `result.seeders` → Seanime tiebreaker). The gap is the **nyaa DB**: ~98% of rows (AnimeTosho bulk-dump sourced) have **null seeders**. Fix = scraper change in the nyaa addon (`/h/Projects/Nyaa`, Python, not git) to fetch live seeders. Moot for TorBox-cached debrid anyway.

---

## Remaining tasks
- **#32 Server-side audio transcode** (DTS/TrueHD/PCM) — *the meaningful one now that REMUX is auto-preferred*: REMUX often carries lossless the native Chromium player can't decode → **silent audio**. Plan A (codec-aware routing: passthrough for AAC/AC3/EAC3/FLAC, route DTS/TrueHD/PCM through the existing `mediastream` transcoder which already does `-c:a aac` + HLS audio-switching). Main work: that transcoder assumes a **local file path** (`internal/mediastream/transcoder/stream.go:450 "-i", ts.file.Path` + hash/keyframe probing) — must accept a **debrid URL** input. Full detail in `remux-audio-support.md`. User preference was **"avoid transcode"** → alternatives are mpv/Tenji (mpv handles everything) or a custom Electron/Chromium build (Denshi-only, heavy — Chromium gates DTS/TrueHD in its codec allowlist, so it's a real rebuild not just an ffmpeg swap; see `denshi-eac3-decode-fix`).
- **#13 P7 watch-party rooms** — user's original "next" before the debugging detour. Same-backend rooms (shared playback state across users on one VPS).
- **#29 Parallel batch+single autoselect search** — `internal/torrents/autoselect/search.go` `searchFromProvider` fires batch then single **sequentially** for finished series (~2 rounds). Parallelize → ~8s→~5s. Modest; aggregator-contention part is infra-bound.
- **#24 per-session module live settings** (settings snapshot at session build), **#25 per-session module eviction** (sessions accumulate, no cleanup) — minor/scale.
- **#15 P9 client-relocation (Denshi)** — vague; likely supersede.

## Open user actions
1. Install the new Denshi build; hard-refresh web — to see the badges.
2. Test concurrent two-client streaming — confirm the adding-stall is gone.
3. Decide #32 direction (transcode vs. mpv-only for lossless REMUX).
4. Optional: nyaa addon scraper to populate live seeders.

## Gotchas / runbook pointers
- Deploy ritual (CLAUDE.md): back up `seanime.db` + `config.toml` first; build arm64 (portable Go `/c/Users/Clin/go-sdk/go`, `CGO_ENABLED=0`); scp → swap → **`sudo chcon -t bin_t seanime`** (else 203/EXEC) → restart → curl 200.
- **Web change ⇒ full chain**: `npm run build` → `web/` swap → arm64 binary (VPS), **and** `npm run build:denshi` → `out-denshi` → `seanime-denshi/web-denshi` swap → `build:win` (Denshi installer). Denshi is a thin client to the VPS; backend-only changes need **no** Denshi rebuild.
- SSH to VPS gets flaky after many rapid connections (or Surfshark VPN — memory `surfshark-blocks-ssh-deploy`); retry / pass `-o ConnectTimeout`.
- AIOStreams config password: in memory only; use transiently, never store. Config API: `GET /api/v1/user/` and `/api/v1/search?type=&id=&format=true` (Basic auth uuid:pw).
