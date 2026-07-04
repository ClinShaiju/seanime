# Fable audit — debrid prewarm / play-latency layer (2026-07-01)

Follow-up audit of the prewarm implementation that landed in 3.8.12 (`prewarm-audit.md` Part 5 = design source of truth). Scope: correctness, performance, stability — driven by three user reports plus independent verification (code review, unit tests, **live VPS journal + production DB inspection**).

**User reports investigated:**
1. Prewarm rows kept for already-finished episodes (n-2 and behind should be cleared).
2. Inconsistent prewarming.
3. Tenji doing a full load despite prewarm / prewarm not triggering.

**Verdict:** the core machinery is healthy — the web/Denshi binge path is verifiably instant end-to-end, the 429 backoff/queue/limiter layer produced **zero TorBox 429s in 7 days of production logs**, hydrate-from-DB and no-createtorrent URL refresh both observed working in production. All three reported symptoms are real, reproduced with evidence, and share two root causes: **(a) no progress-aware cleanup** and **(b) Tenji has no client-side preload trigger, so it rides the 10-minute tick, which structurally lags a binge by one episode**.

---

## 0. Findings index

| # | Severity | Finding | Root cause of report # |
|---|---|---|---|
| F1 | **HIGH** | Watched-episode prewarm rows/entries never cleaned — persist up to 24h behind progress | 1 |
| F2 | **HIGH** | The 10-min tick chases a binge one episode behind (targets `progress+1`, progress syncs at 85%) — it usually re-resolves the episode *currently being watched* and warms the true next-up only by timing luck | 2, 3 |
| F3 | **HIGH** | Tenji's `useDebridPrewarm` hook is dead code — zero call sites; no entry-mount or in-playback preload ever fires from Tenji | 3 |
| F4 | MED | Metadata prewarm silently skipped when a preload is satisfied by DB-hydrate or an existing fresh entry — tier-1 target can be "hot"-badged with cold metadata → full "Loading metadata" at play | 3 (Denshi flavor) |
| F5 | MED | Sessions never leave the prewarm loop — inactive users' targets are re-resolved forever (hourly for releasing shows) | — (background waste) |
| F6 | LOW | Releasing-show 1h selection TTL + 10-min tick ⇒ hourly re-search churn and up to 10-min badge flicker windows | 2 (minor flavor) |
| F7 | LOW | No negative cache — a just-aired target with no torrents yet triggers a full aggregator search every 10 min until releases appear | — |
| F8 | LOW | Badge "hot" (red) flag reflects resolve-time opts, not the live metadata cache (2h TTL vs 24h selection TTL) | — (cosmetic) |
| F9 | INFO | One CDN HEAD 429 observed (handled by GET fallback); `cdnWarmLimiter` holding; **no TorBox 429s in 7 days** | — (good news) |
| F10 | INFO | Minor code nits (stale comment, hydrate bypasses evict cap, mutation of caller opts) | — |

---

## 1. What was verified working (production evidence)

From `journalctl -u seanime` (7-day window) on the VPS:

- **Web/Denshi binge path is instant, every episode.** Jul 01 13:59 → 21:04, Log Horizon (17265) eps 15–22 on `nativeplayer`: every start logs `Using preloaded stream` **and** `Reusing prewarmed metadata parser` (selection + URL + MKV metadata all hot). The @3s client trigger fires ~5s after each play (`Preloading stream episodeNumber=N+1`) and completes in 5–11s.
- **No-createtorrent URL refresh works.** Replays of eps 18/19 hours later: `Refreshed expired preloaded URL (no createtorrent)` (19:29:00, 19:55:44).
- **Shared-DB hydrate works.** Jun 30 21:01:52: `Hydrated prewarm from shared DB (no re-resolve)` → immediate `Using preloaded stream` (post-restart / cold-memory case).
- **Rate safety holds.** Zero TorBox 429s, zero `Preload failed` warnings in 7 days. The serialized drain shows no overlapping resolve bursts. One **CDN** HEAD 429 (Jul 01 20:22:50, `HEAD response not usable, falling back to GET`) was absorbed by the existing fallback — F9.
- **Settings saves no longer wipe** (no wipe events in logs; S2 behaving).
- **Unit tests green**: `TestSelectPrewarmTargets*`, `TestProfileHashFor`, `TestPersistedActiveStream_JSONRoundTrip`; `go vet` clean. (The `download_test.go` failures on Windows are pre-existing temp-dir locking flakes in the *downloader*, unrelated to prewarm.)

---

## 2. F1 — Watched episodes never cleaned out (report #1) — HIGH

### Evidence (production DB, `debrid_prewarms`, queried 2026-07-01 ~21:30 local)

17 rows total; **15 are Log Horizon (17265) episodes 9–23** while the user's progress is ~22:

```
media_id  ep   resolved_at            ttl
17265      9   2026-07-01 00:32:43    24h     ← watched 00:28 that night
17265     10   2026-07-01 00:52:43    24h     ← watched
...every episode of the binge...
17265     22   2026-07-01 20:42:41    24h     ← watched
17265     23   2026-07-01 21:04:36    24h     ← actual next-up (the only useful LH row)
```

13 of 15 LH rows are at-or-behind progress and will sit there for 24h. Nothing in the code ever deletes by progress:

- `SweepExpiredPrewarms` (`internal/debrid/client/prewarm_db.go:211`) is **TTL-only**.
- `GetPrewarmStatus` (`prewarm_db.go:241`) returns **every fresh row** for the account+profile with no progress filter → the UI badges already-watched episodes with flames (the visible symptom).
- In-memory: tick-resolved entries for watched episodes are `Priority` → **never evicted by the speculative cap**, only by TTL (24h for finished shows) (`stream.go:990-1021`). The `lastConsumedKey` drop only covers the one episode actually consumed via the preload path.

### Why rows exist for watched episodes at all

Two writers, both legitimate at write time:
1. The @3s client preload persists ep N+1 while you watch N — after you watch N+1 the row is simply stale.
2. The tick's lag (F2) *re*-resolves the currently-watched episode and persists it.

So the fix is cleanup, not write-avoidance.

### Fix (recommended)

Progress-aware cleanup, wired where progress is already in hand:

- **`buildPrewarmOptsForSession`** (`internal/core/prewarm.go:199`) already has `progress` per media from the collection. After computing targets, call a new `DebridClientRepository.CleanupWatchedPrewarms(userID, mediaId, keepFromEp)` per candidate show with `keepFromEp = progress` — i.e. **delete DB rows and in-memory entries with `episode_number < progress`** (keeps the last-watched episode for instant replay — matches the user's "n-2 and behind" ask — and everything ≥ next-up).
- Optionally also call it from the AniList progress-update handler for immediacy; the tick version alone bounds staleness to 10 min.
- Belt-and-braces: filter `GetPrewarmStatus` output against the same rule so badges are progress-correct even between cleanups (status endpoint has no collection access today — passing a `minEpisodeByMedia` map from the handler, which can read the session collection, is the cheap route; or skip this if the sweeper fix is deemed enough).

**Caveat (accept + document):** `debrid_prewarms` rows are **account-shared**. Deleting on user A's progress can evict a row user B (same TorBox account, lower progress) would have reused — cost is one ~10s re-resolve for B. With the current single-household deployment this is fine. `// ponytail: progress-cleanup is per-account, not per-user; add user_tags refcounting if multi-user divergence ever matters`.

---

## 3. F2 — The tick chases binges one episode behind (reports #2 & #3) — HIGH

### Evidence — the Tenji binge, correlated line by line

Tenji session Jun 30 21:36 → Jul 01 05:32, Log Horizon, `playbackType=externalPlayerLink`. **12 of 14 episode starts were cold** (`Starting stream` = full ~10–20s resolve); only eps 6 and 11 hit (`Using preloaded stream`).

The mechanism, proven by timestamp correlation (ticks fire at :X2:41):

| Event | Time | What happened |
|---|---|---|
| User starts ep 9 (cold) | 00:28:58 | progress still 8 |
| Tick | 00:32:41 | target = progress+1 = **9** → full resolve of the episode already playing (row created 00:32:43) |
| progress→9 (85% sync) | ~00:47 | |
| User starts ep 10 (cold) | 00:50:32 | tick hasn't run since progress sync |
| Tick | 00:52:41 | target = **10** → resolves the episode already playing (row 00:52:43) |
| progress→10 | ~01:08 | |
| Tick | 01:12:41 | target = **11** → resolved 01:12:48 |
| User starts ep 11 | 01:13:28 | **HIT** — tick landed in the ~4-min window between progress sync and click |

Episodes are ~21 min; progress syncs at 85% (~18 min in); the next click comes ~3–4 min later. A hit requires a tick inside that window ⇒ **~20–35% structural hit rate — observed 2/14**. Every miss *also* wastes a full resolve on the episode already being watched (search + requestdl + a stale row feeding F1).

Web/Denshi never sees this because the client fires the @3s next-episode preload. Tenji fires nothing (F3), so the tick is its only feeder.

### Fix (recommended) — server-side next-episode chaining

The staged "event-driven loop" item, in its minimal high-value form: **when a real (non-preload) auto-select `startStream`/`playPreloadedStream` succeeds for episode N, kick a background preload of episode N+1** for that user (resolve the AniDB episode the same way `buildPrewarmOptsForSession` does; `Priority: true`, `PrewarmMetadata: true` — it *is* the highest-certainty target).

- Universal: fixes Tenji, future clients, and external players **without any client deploy** (relevant given the Tenji EAS/hermesc build pain).
- Zero duplication: web's @3s client call dedupes against it via the existing in-flight/fresh-key check.
- Kills the wasted mid-episode resolve: by the time the tick fires, N+1 is already warm and the tick's `progress+1 == N` target hits the fresh in-memory entry for N (skip) — or better, pair with the F1 cleanup so N-1 targets vanish.
- The 10-min tick then reverts to its intended cold-start/just-aired role.

Secondary (optional): have the tick skip a target when continuity shows that exact episode currently in progress (`WatchHistoryItem.EpisodeNumber == target && CurrentTime > 0`) — one guard in `selectPrewarmTargets` candidates; mostly redundant once chaining lands.

---

## 4. F3 — Tenji's prewarm hook is dead code (report #3) — HIGH

`seanime-tenji/src/components/features/torrentstream/use-debrid-prewarm.ts` exists and is correct (fires `preload: true`, `playbackType: "externalPlayerLink"` — which `canReusePreloadedStream` accepts), **but nothing imports it**. Grep of the Tenji tree: the only matches are the hook file itself, the status hook, and the badge. Neither the entry screen nor the player's next-episode logic calls `prewarm()`. The web client wires it in `video-core.tsx` (@3s, with `prewarmMetadata: true`) and `debrid-stream-page.tsx` (entry mount); Tenji ported the hook but not the call sites.

**Fix:** F2's server-side chaining makes wiring Tenji optional (recommended order: do F2 first, skip the Tenji OTA). If/when a Tenji build ships anyway, wire `prewarm()` at (a) entry-screen mount for next-up and (b) ~3s into playback for N+1 — both one-liners with the existing hook.

---

## 5. F4 — Metadata prewarm silently skipped on hydrate / fresh-entry upgrade — MED

Two paths accept a preload request flagged `PrewarmMetadata: true` and complete **without ever parsing metadata**:

1. **DB-hydrate front-gate** — `preloadStreamWith` returns immediately when `hydratePrewarmFromDB` succeeds (`stream.go:1087-1089`), skipping the `PrewarmStreamMetadata` block at `stream.go:1196`. Post-restart (or cross-user reuse), the tier-1 target hydrates → metadata never warmed.
2. **Fresh-entry skip** — an existing fresh entry only gets its `priority` upgraded (`stream.go:1054-1061`); an incoming `PrewarmMetadata: true` for an entry originally created without it (e.g. entry-mount preload) is dropped.

Consequences: the play pays the full "Loading metadata" step despite a "prewarmed" state, and worse, the badge can show **red/hot falsely** — in-memory the stored `opts.PrewarmMetadata` may be true while the parser cache is cold (hydrate path stores the *caller's* opts: `prewarm_db.go:138-141`).

**Fix (3 lines each):** in both paths, if `opts.PrewarmMetadata && streamUrl != "" && s.ds(opts) != nil`, call `s.ds(opts).PrewarmStreamMetadata(streamUrl)` — it's idempotent (parser/HEAD caches + `cdnWarmLimiter` already guard re-entry).

---

## 6. F5 — Prewarm never forgets a user — MED

`prewarmContinueWatchingStreams` iterates `a.sessions` (`internal/core/prewarm.go:169-182`), and sessions live for the **server process lifetime** (`session.go` — deleted only on logout/user-deletion). A user who logged in once and left keeps getting their 3+3 targets warmed forever:

- Finished-show targets: one re-resolve per 24h TTL expiry — mild.
- **Releasing-show targets: 1h TTL ⇒ ~24 full resolves/day/target for a user who may not return for weeks.**

**Fix:** one filter — skip candidates whose continuity `TimeUpdated` is older than ~14 days (in `buildPrewarmCandidates` or `selectPrewarmTargets`). A returning user is re-picked the moment they watch anything. `// ponytail: 14d recency cutoff; make it a setting if anyone ever asks`.

---

## 7. F6/F7 — releasing-show churn & missing negative cache — LOW

- **F6:** `preloadSelectionTTLReleasing = 1h` (`stream.go:242`) + 10-min tick ⇒ for each releasing target: hourly re-search, plus a 0–10 min dead window after expiry (row swept + memory expired, before the next tick re-resolves) where the badge disappears and a play goes cold — one flavor of "inconsistent prewarming". The 1h value exists so a better release can supersede; releases materially change only in the first ~day. Consider 3h, or keep 1h only while the episode is <48h old, else 24h.
- **F7:** a just-aired target with no torrents yet fails the search and retries **every tick** (documented as intended in prewarm-audit §Part 5, but unbounded): a show whose releases lag by a day costs ~144 aggregator searches. Cheap fix: per-key failure timestamp in the `StreamManager`, skip re-attempts for 30–60 min. `// ponytail: map[string]time.Time negative cache, no persistence`.

---

## 8. F8/F9/F10 — minor

- **F8:** `GetPrewarmStatus` reports `Metadata` from stored opts; the actual parser/HEAD cache TTL is 2h (`metadataCacheTTL`) vs 24h selection TTL, so the red badge can overstate for up to 22h (plus the F4 hydrate case). Cosmetic — fix F4 first; optionally have status ask the directstream manager whether the URL's parser entry is live.
- **F9 (good):** the only rate event in 7 days is a single CDN HEAD 429 during a tick resolve, absorbed by the existing GET fallback. TorBox-side: clean. The L2 backoff / drain queue / cdnWarmLimiter stack is doing its job.
- **F10 nits:**
  - `internal/core/prewarm.go:19` comment "cache TTL is 15m" is stale (metadata cache is 2h now).
  - `hydratePrewarmFromDB` inserts into `preloads` without `evictIfNeededLocked` (`prewarm_db.go:143-146`) — non-priority hydrates bypass the speculative cap. Bounded in practice; one-line fix if touched.
  - `PrewarmStreams` drain mutates caller opts (`opts.Preload = true`) — fine today (core owns them), fragile if ever reused.
  - Play-path hydrate probes the URL, then `playPreloadedStream` can probe again for direct-URL entries — at most one redundant 1-byte range GET; ignore.

---

## 9. Recommended fix order (smallest diffs first, by impact)

1. **F2 chaining** — `startStream`/`playPreloadedStream` success → background preload of ep N+1 (reuse the AniDB-resolution snippet from `buildPrewarmOptsForSession`). *Fixes Tenji + inconsistency with zero client deploys; ~40 lines in `stream.go` + a small shared helper.*
2. **F1 cleanup** — `CleanupWatchedPrewarms(mediaId, keepFromEp)` (memory + DB `episode_number < progress`), called per-candidate from the tick where progress is already known. *Fixes the lingering-rows/badges complaint; ~30 lines (repo func + one call site + a `DeleteDebridPrewarmsBelow` DB helper).*
3. **F4 metadata-on-hydrate** — two 3-line guards.
4. **F5 recency cutoff** — one filter line + constant.
5. F6 TTL tune / F7 negative cache / F10 comment — opportunistic.

Not recommended now: per-user row tagging/refcounting for cleanup safety (single-household deployment; revisit with P4 per-user keys), Tenji client wiring (superseded by #1), event-driven replacement of the whole tick (the tick remains correct for cold-start/just-aired once #1 lands).

---

## Appendix — how each user report maps

| Report | Root cause | Fix |
|---|---|---|
| "unnecessary prewarm saving on already finished episodes (n-2 and behind should be cleared)" | F1 (no progress cleanup) + F2 (tick re-resolves the currently-watched ep, creating those rows) | Fix #2 + #1 |
| "inconsistent prewarming" | F2 (tick timing lottery), plus F6's 0–10 min dead windows on releasing shows | Fix #1 (+ F6 tune) |
| "Tenji doing full load despite prewarm / prewarm not triggering" | F3 (hook never called) ⇒ tick-only ⇒ F2's ~2/14 hit rate; Denshi flavor also F4 (hot badge, cold metadata after restart) | Fix #1 + #3 |
