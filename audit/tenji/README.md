# Tenji iOS client audit — 2026-07-10/11

Two-track audit of `H:/Projects/seanime-tenji` (fork `ClinShaiju/seanime-tenji`).

- **Tenji HEAD audited:** `65f8baf` (v0.1.24, 2026-07-09)
- **Parity baseline:** `seanime-web` @ `5a8a9b74` (v3.9.5 HEAD — the exact UI Denshi bundles)
- **Method:** two orchestrated multi-agent workflows.
  - *App audit:* 18 disjoint sweep scopes (routes, api client/hooks, ws, player ui/lib, native mpv + other native modules, entry/media, discover/lists, manga, nakama, streams, offline/OTA, state, ui-kit, perf + security crosscuts) → semantic dedup → adversarial verification (3-lens panel for critical/high, single refuter otherwise).
  - *Parity audit:* 10 feature-area comparisons + a commit-by-commit delta safety net over `20596e8b..5a8a9b74`; every claimed gap adversarially re-verified in both directions (feature really exists in web; Tenji really lacks it incl. renamed reimplementations; genuinely applicable to iOS).
- Prior-audit items (tenji-audit.md T1–T13, all fixed) and the v0.1.24 known-deferred parity list were excluded from rediscovery by construction (deferred items appear in parity.md as `deferred-known`, re-verified still missing).

## Reports

| File | Contents |
|------|----------|
| [findings.md](findings.md) | App audit: **52 confirmed findings** (0 critical, 3 high, 19 medium, 30 low), 5 refuted; every finding adversarially verified |
| [parity.md](parity.md) | Parity vs Denshi/web 3.9.5: **16 missing · 6 partial · 7 deferred-known** (post-dedup), 57 ported confirmed, 25 n/a, 0 dropped |
| `raw/` | Unabridged per-scope sweep notes (18 app scopes + 10 `parity-*` areas), incl. near-misses rejected and why |

## TL;DR — act on these first

**App (findings.md):** no criticals; all 3 highs are native-side.
1. **H1** — mpv wakeup-callback **use-after-free** on every player teardown (`MPVLayerRenderer.swift:183`): unregister the callback synchronously in `stop()`.
2. **H2** — **unsynchronized Swift dictionaries** in the download module mutated from the URLSession delegate queue *and* the JS-call queue → EXC_BAD_ACCESS; serialize access.
3. **H3** — manga reader default long-strip mode mounts **every page of a chapter at once on iOS** (the image windowing is Android-only) → jetsam/OOM on long chapters.
4. Medium standouts: WS reconnect silently skipped after login/logout while UI says "connected" (M1); session bearer token in cleartext through the shareable diagnostic-log export (M2); server/session tokens in plaintext MMKV instead of Keychain (M3); failed video loads spin forever — iOS never handles `MPV_EVENT_END_FILE` (M4).

**Parity (parity.md):** top recommended ports, ranked:
1. **Watch-room follower seek-cooldown throttle (S)** — Tenji still ships the pre-fix directstream seek→rebuffer→seek thrash loop web fixed in `159a4efe`.
2. **Loading-screen artwork (M)** — ani.zip backdrop + clearlogo via the live `GET /api/v1/anizip-artwork/:id` endpoint; flagged independently by 4 of 10 areas.
3. **Debrid cache name-flag detection (S)** — Cached/Uncached picker filter is invisible on RealDebrid/AllDebrid; port `getTorrentCacheStatus()` string heuristics.
4. **AniList rate-limit countdown banner (S)** — server already broadcasts the WS event; Tenji's router drops it.
5. **Onlinestream auto provider/server retry-cycling (M)** — web self-heals flaky providers; Tenji is manual-retry only.

_No code was changed by this audit._
