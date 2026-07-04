# Debrid Prewarm — Audit & Improvement Plan

Audit of the debrid play-latency / prewarm layer (`main`, 2026-06-24; revised after review). Parts:

1. **What it does today** — every step, timing, TTL (corrected after review).
2. **What to improve** — the agreed plan (selection, rate limits, metadata, the shared DB).
3. **Research** — how comparable apps and the streaming literature solve this.
4. **Server-wide shared prewarm DB** — the persist-and-reuse design.
5. **Implementation status** — what's landed vs staged.
6. **Appendix** — review comments and their resolutions.

> Scope rule: per [[stream-selection-quality-over-speed]], nothing here reorders or filters torrent candidates by cache/instant status. Prewarm only resolves the *already-chosen* auto-select result earlier and keeps it warm.

---

## Part 1 — Current implementation (full walkthrough)

Two prewarm feeders write into **one per-user in-memory preload cache**, consumed by the native/external player at play time.

```
                 ┌─────────────────────────── feeders ───────────────────────────┐
  (server) continue-watching loop ─┐
  (client) next-episode @3s ───────┤
  (client) entry-page mount ───────┼──► preloadStream() ──► preloads map (per user)
  (client) hero-card hover ────────┘                              │
                                                                  ▼
                                          startStream() ──► canReusePreloadedStream? ──► playPreloadedStream()
```

### 1.1 Server-side continue-watching loop — `internal/core/prewarm.go`

A goroutine started once at boot (`startContinueWatchingPrewarmLoop`):

1. **Initial delay 30s** (`prewarmInitialDelay`) — lets the collection + debrid settings load.
2. Then **every 10 min** (`prewarmInterval`). **What the tick actually is:** a *target-discovery* poll, not a refresh — `preloadStream` skips any key still within its TTL, so warm selections are a no-op on the tick (no re-resolve, no extra TorBox calls). Its only job is to (re)pick the 3 most-recently-watched in-progress shows from watch history as recency shifts across sessions. Because the **client already fires the next-episode preload at 3s into playback**, the server loop matters only for the **cold case** (warming the continue-watching row on app-open / after a restart). ⇒ this should become **event-driven** (warm on login / progress-change / just-aired), not a blind 10-min timer — see §2.1.
3. Each tick (`prewarmContinueWatchingStreams`):
   - No-op if no debrid provider or `PreloadNextStream` off.
   - **Per-user**: admin + every active `UserSession` (deduped by `UserID`); each user's targets come from *their* collection + continuity and cache into *their* `StreamManager`.
   - `buildPrewarmCandidates` cross-references watch-history recency (`GetWatchHistory().TimeUpdated`) against the AniList collection (progress + `CurrentEpisodeCount`), keeping only `CURRENT`/`REPEATING`. ⚠️ **Known bug (tracked separately):** recency targeting is unreliable — see §2.6.
   - `selectPrewarmTargets`: most-recent first, skip not-started/caught-up, take top **3**. Target = `progress + 1`. **One episode per show** (confirmed — nothing else).
   - Resolves the real AniDB episode (`FindEpisodeByNumber`) so the cache key matches the client's play-time key.
   - Flattened → `PrewarmStreams(ctx, allOpts)`.

Each option: `AutoSelect: true`, `Preload: true`, `Priority: true`, `PrewarmMetadata: false` (§1.8).

### 1.2 The fan-out — `Repository.PrewarmStreams`

**(current)** Loops over targets and calls `preloadStream` for each; each spawns a goroutine and returns immediately, so **all targets resolve concurrently, uncapped** (N_users × 3). This simultaneous burst is a prime 429 suspect → replaced by a serialized queue in §2.2.

### 1.3 The resolve — `StreamManager.preloadStream` (`debrid/client/stream.go`)

Keyed by `preloadKey = "mediaId|episodeNumber|aniDBEpisode"`.

1. Under `preloadMu`: skip if a fresh entry (within TTL) or an in-flight resolve exists for the key (dedup). A `Priority` call upgrades an existing speculative entry in place.
2. Async:
   - **Selection** (the expensive part): `findBestTorrent` → `autoSelect.FindBestTorrent`:
     - a **torrent search** via the AIOStreams aggregator — dominant latency ~1.4–3s, **external, not TorBox**;
     - cache status from the source's name flag (free), falling back to `GetInstantAvailability` (`/checkcached`) only for unflagged torrents (0 for fully-flagged AIOStreams);
     - quality ranking + file selection (source of truth, untouched).
   - **Add** (skipped for direct `StreamUrl`): `AddTorrent` → dedup via `getTorrentsCached` (120s mylist cache) then `POST /createtorrent` if absent. **The TorBox client is a singleton on the Repository**, so this mylist cache + the append-on-add are **shared across all users on the same key** (today: one shared key).
   - **Resolve URL**: `GetTorrentStreamUrl` polls until ready, then `GetTorrentDownloadUrl`. The poll is under the 300/min/endpoint budget (a contributor under concurrency, but `createtorrent` 60/hr binds first).
3. Stores a `preloadedDebridStream` (`resolvedAt`, `ttl`, `urlResolvedAt`, `priority`, torrent + `torrentItemId` + `fileId` + `filepath` + `streamUrl`).
4. **Metadata pre-parse** (`if opts.PrewarmMetadata`): **never runs today** — §1.8.

### 1.4 TorBox poll loop — `GetTorrentStreamUrl` (`debrid/torbox/torbox.go`)

- **Poll-immediately with backoff** `500ms → 1s → 2s → 4s` (cached torrents ready on poll 1).
- Each poll = `GET /torrents/mylist?bypass_cache=true&id=`.
- On ready: a **1s settle sleep** then `GET /requestdl` (+ **one extra** `getTorrent` to map short-name `fileId` → numeric id). Both are removable: the sleep is a hedge (drop after verifying `requestdl` doesn't 404 immediately), and the round-trip vanishes if we cache the numeric file-id at selection time — see §2.2.

### 1.5 Consume & lifecycle — `startStream` / `playPreloadedStream`

**(current)** On a real play, if `canReusePreloadedStream` (native/external only):
- A **different** episode starting drops the previously-consumed entry; same-episode replays keep theirs.
- A **hit** requires fresh + `debridStreamOptionsMatch` (media+ep+aniDB + same `AutoSelect` intent; manual ⇒ same torrent+file).
- A consumed entry is currently **deleted on episode end**, and `playPreloadedStream` re-resolves the URL only after `urlRefreshTTL` (2h).

⇒ Both behaviors change under persist-and-reuse (§2.4): keep entries until TTL/dead-link, and validate the cached link with a cheap probe instead of a blind time-based re-resolve.

### 1.6 TTLs & eviction (current)

| Knob | Value | Meaning |
|---|---|---|
| `preloadSelectionTTL` | **24h** | selection lifetime, finished show |
| `preloadSelectionTTLReleasing` | **1h** | selection lifetime, currently-releasing show |
| `urlRefreshTTL` | **2h** | trust window for a resolved link (TorBox links ~3h) |
| `maxSpeculativePreloads` | **8** | cap on **non-priority** entries only |
| parser / `streamInfoCache` TTL | **15m** | metadata parser + HEAD cache — **too short**; metadata is immutable per URL → raise to URL lifetime (§2.4) |
| `mylistCacheTTL` | **120s** | TorBox mylist dedup cache (AddTorrent) |

`evictIfNeededLocked` TTL-sweeps both classes then caps only speculative entries (FIFO). Priority (continue-watching) entries are uncapped and never evicted.

### 1.7 Persistence — `persistActiveStream` / `loadPersistedActiveStream`

The last active stream is snapshotted to `DebridActiveStream` (per `UserID`) and restored into the cache (as priority) when the user's `StreamManager` is created — instant reconnect after a restart. Part 4 generalizes this into the shared prewarm table.

### 1.8 Metadata prewarm + CDN warm — **dead code today**

`directstream/manager.go` has a full path: `PrewarmStreamMetadata(url)` caches the HEAD (`streamInfoCache`), MKV-parses structure + loads font attachments into RAM (`parserCache`, 15m), then `warmStreamStart` range-GETs the playable start (bitrate-scaled, **16–48MB**). It runs only when `opts.PrewarmMetadata == true`, which **no caller sets** (handler hard-codes `false`; `core/prewarm.go` sets `false`). It was disabled because the per-user × 3-shows font fan-out 429'd the CDN. **Consequence:** a continue-watching hit gives instant *selection* but still pays the play-time metadata parse + cold first-range fetch. Re-enabled (tier-1 only) in §2.3.

### 1.9 TorBox API call inventory

| Action | Endpoint | When | Count |
|---|---|---|---|
| dedup check | `mylist?bypass_cache=false` | every `AddTorrent` | 1 (cached 120s, shared) |
| add | `POST /createtorrent` | uncached only | 0–1 |
| poll | `mylist?bypass_cache=true&id=` | poll loop | 1–N |
| get link | `requestdl` (+1 `mylist` for fileId) | once ready | 1–2 |
| cache check | `checkcached` | unflagged only | 0–1 |

**(current) No global limiter, no 429 backoff, no concurrency cap.** A 429 surfaces as `code≥400 → error` and **aborts** the resolve. Fixed in §2.2.

### 1.10 Frontend feeders (`seanime-web`)

`useDebridPrewarm` (gated on debrid + `preloadNextStream` + preloadable player; per-mount dedup; `cancel()` clears the pending debounce timer on each new call).

- **Next-episode @3s** — `video-core.tsx`: `preloadNextEpisode()` once per playback at `currentTime ≥ 3` (native player, not playlist).
- **Entry-page mount** — `debrid-stream-page.tsx`: prewarms next-up (auto-select only).
- **Hover** — **only the continue-watching hero card is live** (600ms debounce; each new hover cancels the prior pending timer, so sweeping the row only fires the card you settle on). The **entry-page episode-grid hover is wired but a NO-OP** (`handleEpisodeHover` returns immediately) — it was dropped because resolving a stream per hovered card blew `createtorrent` 60/hr (429). *(This corrects the earlier draft, which described the grid hover as active.)*

### 1.11 Cache-wipe trigger (current)

`ClearAllPreloads()` runs at the top of `InitializeProvider`, called on **every debrid-settings save** — so any toggle (preferred resolution, `PreloadNextStream`) discards the whole warm cache and forces a cold re-resolve. Fixed in §2.1 (S2).

---

## Part 2 — Improvement plan (agreed)

Each item tags the review comments it resolves (see Part 6). Status: ✅ landing now · ⏳ staged.

### 2.1 Prewarm selection — `internal/core/prewarm.go`  ⏳

| # | Change |
|---|---|
| **S1 (revised)** | **Confidence tiers + just-aired axis.** Targets = `{next-up of the last-3-watched in-progress shows}` ∪ `{just-aired episodes of tracked shows}`. `progress` is used only to pick *which* episode (`progress+1`), **not** as a ranking signal — a show you're 1 episode into is as eligible as one you're deep in. Tier-1 (most-recent) gets the full treatment (selection + URL + metadata + CDN); tiers 2–3 selection-only. |
| **S1-timing** | A **just-aired** episode has no torrents *at* broadcast (releases lag the airing), so schedule its prewarm **~1h after air**, not at air time. |
| **S2** | **Stop wiping on benign settings saves** — `ClearAllPreloads` only when provider/account/**API key** changes (diff old vs new `DebridSettings`). |
| **S3** | **Defer the uncached `createtorrent`.** Warm cached selections eagerly; for uncached next-episode targets, defer the add (which starts the TorBox download, pinning an active slot) to mid-episode. Rule-safe — only the chosen torrent's download *timing* moves. |
| **S4** | **Drop the episode-grid hover prewarm** — **already done** (it's a no-op, §1.10). Remaining: every feeder should do a cheap **DB existence check before the full search pipeline** (§2.4) so duplicates never re-resolve. |
| **S5** | **Local-file filter** — skip targets whose next episode is already in the user's local library. |
| **Make the loop event-driven** | Replace the blind 10-min poll with warm-on-login / warm-on-progress-change / just-aired triggers (the @3s client preload already covers active watching). |

### 2.2 TorBox rate safety — `debrid/torbox/torbox.go` + the prewarm scheduler  ✅ (landing now)

- **L2 — 429 backoff (no jitter).** In `doQueryCtx`, detect `429`, honor `Retry-After` (fallback exponential backoff base→cap), retry up to N. Universal safety net for plays *and* prewarms. *(No jitter — single-keyed client; the serialized queue removes the lockstep-retry case jitter would address.)*
- **Queue, not concurrency+jitter (replaces L3).** `PrewarmStreams` drains its targets through a repo-level `rate.Limiter` (`golang.org/x/time/rate`, already a dep) in one background goroutine — kickoffs spaced so the tick no longer hits TorBox simultaneously. Client preloads (play @3s, hero hover) stay direct. *(Supersedes the semaphore+jitter idea; a single drainer needs no jitter.)*
- **C8 — drop the 1s settle sleep** (after verifying `requestdl` doesn't immediately 404) and **cache the numeric file-id** at selection time to delete the extra `getTorrent` round-trip.
- **createtorrent budget (L4)** — a rolling-hour counter near **58/hr** (small margin under 60 — not exactly 59, to absorb our-counter-vs-TorBox-window skew and L2 retries); the **prewarm path** skips/defers when near budget. Real plays are low-rate and not hard-blocked (they rely on L2). Endpoint ceiling ~**295/min**.

### 2.3 Re-enable metadata prewarm — `core/prewarm.go` + `directstream/manager.go`  ⏳

- **M1** — `PrewarmMetadata: true` for **tier-1 only** (one target/user). Kills the font-burst that caused the 429 while removing the "Loading metadata" gap for the most-likely click.
- **M2** — keep `warmStreamStart` (CDN warm), now **behind the limiter** (§2.2) and the first-parse guard; serve cached metadata instantly on a hit so audio/subtitle tracks are ready and the buffer→debrid handoff is clean.
- **M3** — structure-only parse (skip attachment bytes) so *all* tiers can get instant metadata cheaply. Bigger `mkvparser` change → after M1.

### 2.4 Persist-and-reuse + shared DB — Part 4  ⏳

The spine for many comments (C7/C9/C10/C13/C14/C15/C17/C19): a resolved link stays valid ~3h regardless of consume/episode state, so **never discard on consume, episode-end, or settings-save; reuse until the link is actually dead; validate with a cheap probe instead of re-calling the API.** Built as the server-wide table in Part 4 (account-partitioned, profile-gated, refcounted torrent lifecycle). Includes: keep-until-TTL, DB front-gate, link-validity probe, and **raising the parser/HEAD cache TTL to the URL lifetime (~2–3h)** (C12) since metadata is immutable per URL (watch total RAM from font attachments).

### 2.5 What can hit limits, and how to fit

| Pressure | Source | Limit | Mitigation |
|---|---|---|---|
| `createtorrent` burst | uncached prewarms; cold-start after a wipe | **60/hr** | S2, queue (§2.2), L4 budget 58/hr, A1 share selections |
| endpoint flood | mylist polls × concurrency | **300/min/endpoint** | L2, queue, 120s mylist cache, ~295/min ceiling |
| CDN 429 | metadata/font fan-out | CDN-side | M1 (tier-1), limiter, client font cache (done) |
| active-torrent cap | prewarming uncached starts downloads | **1/3/5/10 by tier** | S3 (defer uncached), L4 |
| abuse / bandwidth | `warmStreamStart` 16–48MB × targets | byte-accurate, 30-day | M2 (tier-1 + first-parse guard); don't re-warm a fresh URL |

### 2.6 Tracked separately — watch-history recency bug

Recency targeting is unreliable (the user reports prewarm not hitting the right shows). The native player *does* write continuity on `VideoStatusEvent` (`videocore/effects.go:102`), so likely causes: the `CURRENT`/`REPEATING`-only filter excluding actively-watched shows in other list states; the external-player/mobile path not emitting status updates; or `WatchContinuityEnabled` off → empty bucket. **Needs a focused repro (dump `GetWatchHistory()` vs actual views) — deferred, not part of this implementation pass.**

---

## Part 3 — Research

### 3.1 TorBox limits (authoritative)

| Limit | Value | Note |
|---|---|---|
| Default per-endpoint | **300 / min / API key** | counted per endpoint; no edge limiting |
| Creation (`createtorrent`) | **60 / hour / token** | binding constraint for prewarm |
| On exceed | **429** | use `Retry-After` / backoff; status-code doc lists 200/400/403/500, so detect by code |
| Active torrents | **1 / 3 / 5 / 10** by tier | uncached prewarms consume these |
| IP | **not tracked** | links are **not IP-locked** → shareable across devices (Part 4.2) |
| Free tier | 1 add/24h, 1 active, 10/month | prewarm is paid-tier-only |

**Abuse system:** dynamic thresholds above guaranteed floors; allowed within the **99th percentile**; **4 strikes → ban + key revoked**. Bandwidth tracked **byte-accurately** (CDN/Storage/Zips/WebDAV), 30-day retention, catches top **0.001%**. ⇒ keep speculative CDN warming bounded.

### 3.2 Debrid ecosystem

- **Debrid Media Manager** — ships the pattern Seanime lacked: a **global rate limiter** (240ms min interval, RD) + **exponential backoff** on 429. → §2.2.
- **Comet-uncached** — literal **"Auto Cache Next"** + binge-group recognition; its failure mode (*"the request must finish in the loading screen; Stremio won't wait 20s"*) is exactly what Seanime's **server-side, ahead-of-time** resolve avoids — a structural advantage worth keeping.
- **AutoStream** — advertises next-episode preloading over RD/AD/Premiumize/TorBox.
- **Shared cache DBs** (Torrentio / MediaFusion / StremThru) — replaced dead instant-availability APIs; relevant only for a future cache *badge* (not selection — the quality rule forbids it).
- **Real-Debrid** — capped active downloads 42→6 after mass cache-guessing → reinforces S3.

### 3.3 Streaming / CDN literature

**Netflix's 5-stage next-episode pipeline** vs Seanime:

| Netflix | Seanime | Status |
|---|---|---|
| ① Predict continue | recency selection | ✅ (improve via S1 / fix §2.6) |
| ② Prepare metadata | `PrewarmStreamMetadata` | ⛔ disabled → M1 |
| ③ Warm CDN | `warmStreamStart` | ⛔ disabled → M2 |
| ④ Prefetch first chunks | FileStream seed | ❌ not built (A4) |
| ⑤ Re-check still watching | — | ⏳ S3 defer = a crude proxy |

- **Sequential next-episode is the strongest signal** — don't over-model.
- **"Throttle/cancel matters more than start."** Seanime had good cancellation, weak throttling — §2.2 closes it.
- **Bandwidth-adaptive depth** (arXiv 2209.02927), **CacheFlix LSTM** (off-peak prefetch) — overkill at this scale; S1 tiers are the lazy version.
- **Thundering-herd warming** (Fastly/HBO) — what `warmStreamStart` does for the TorBox CDN.

---

## Part 4 — Server-wide shared prewarm DB

**Proposal:** replace the per-user in-memory `preloads` map with a **server-wide DB table**, content-keyed, tagged with the user(s) it serves, TTL-tracked; reuse existing entries; keep past consumption (expire by TTL/dead-link, not on use).

**Verdict:** worth doing for **selections *and* resolved links**, **partitioned by TorBox account**, **profile-gated**, with a **reference-counted torrent lifecycle**. Headline wins: eliminate redundant searches, share resolved links (TorBox isn't IP-locked), survive restarts. The hard part is the torrent-deletion lifecycle, not the table.

### 4.1 Account-partition rule

A prewarm is reusable only by users on the **same TorBox account** (the `torrentItemId`, cache, and limits all live behind the key):
- **Shared server key** (today's only wired path; per-user keys modeled in `UserOverrides.DebridApiKey` but stream-path wiring "lands with P4"): users share one account → selections + the 60/hr + active-slot budget → dedup helps most.
- **Own key**: separate account → no cross-user reuse, but persistence still wins for that user.

⇒ Key the table by `accountHash = sha256(apiKey)` (store the hash, never the raw key). Reuse only within the same `accountHash`; tag `userId`(s) for the refresh loop + refcount.

### 4.2 What's shareable

| Field | Shareable within an account? | Why |
|---|---|---|
| **SELECTION** (`torrentItemId`+`fileId`+torrent+filepath) | ✅ | the torrent is added once; any consumer streams the same item |
| **Resolved CDN URL** (`requestdl`) | ✅ | **TorBox doesn't track IP**, so links aren't IP-locked — one link plays on multiple devices concurrently (confirmed in practice). The watch-room "follower never loads" was a **Nakama cross-platform bug**, not a URL limit; the `GetUserStreamShare` comment blaming link contention is **misleading and should be corrected**. |
| **Metadata / CDN warm** | ✅ per URL | URL-keyed caches; shared naturally |

⇒ **Cache + share both selection and URL.** Serve the cached URL while fresh; re-resolve from the shared `torrentItemId` only when stale/dead (cheap, no `createtorrent`). No per-consumer re-resolve on the hot path.

### 4.3 Effect on TorBox limits (honest)

| Endpoint | Today | With DB | Net |
|---|---|---|---|
| **search** (AIOStreams, not TorBox) | per user per target | per (account, content, profile) | ★ big latency/aggregator win |
| **createtorrent** (60/hr) | deduped ≤120s by the shared mylist cache; re-bursts on restart/wipe | deduped to selection-TTL (24h) + survives restart | modest (mostly kills the cold-start re-burst) |
| **mylist poll** | per resolve | fewer resolves | moderate |
| **requestdl** | per play (and per consumer) | shared within `urlRefreshTTL`, one resolve serves all | reduced |

`createtorrent` is already largely deduped today (singleton client + 120s mylist cache); the DB's win there is the 120s→24h extension + restart survival. The big win is **search elimination**. (When per-user keys land in P4, the client splits per-account and the in-memory cross-user dedup disappears — then this DB becomes the *only* cross-user dedup.)

**Risk of keeping selections alive:** each kept selection is a torrent on the account. **Cached/completed** torrents don't count against the active cap (free to keep); **uncached** ones occupy active slots (keep sparingly + short-TTL, pairs with S3). Bandwidth accrues only on transfer, not on holding a selection.

### 4.4 The real blocker: torrent-deletion lifecycle

`CancelStream(RemoveTorrent:true)` → `DeleteTorrent` removes the item, fired by the native-player teardown/abort callback (`stream.go:341,1237`) and the explicit "remove" button. On a shared account, User A's teardown can delete the item User B / the DB references → dead `torrentItemId`.

Mandatory: **reference-count** by `(accountHash, torrentItemId)` across live streams + unexpired rows; `DeleteTorrent` only at count 0. *(Lazy-correct version: stop removing on normal teardown — only on the explicit user action — and let TTL + a sweeper reclaim.)* On consume, if `GetTorrent(itemId)` shows the item gone, treat as a miss and re-resolve.

### 4.5 Schema (generalize `DebridActiveStream`)

```
debrid_prewarm
  id
  account_hash     TEXT (index)   -- sha256(apiKey); sharing + limit partition
  media_id         INT
  anidb_episode    TEXT
  profile_hash     TEXT           -- auto-select profile fingerprint; reuse only on match
  user_tags        TEXT (json)    -- which users want it (refresh loop + refcount)
  data             TEXT (json)    -- selection + resolved url: torrentItemId, fileId, torrent, media, filepath, streamUrl
  resolved_at      DATETIME
  url_resolved_at  DATETIME
  ttl_nanos        INT
  UNIQUE (account_hash, media_id, anidb_episode, profile_hash)
```

- **Reuse:** look up by `(accountHash, mediaId, anidbEpisode, profileHash)`; if fresh, reuse selection (re-resolve URL only if stale/dead). Else resolve + upsert.
- **Expiry:** a sweeper (the existing tick) drops rows past `ttl`; releasing-show rows carry the 1h ttl.
- **`profile_hash`** keeps the quality rule mechanical: a different-profile user misses the row and resolves their own.
- The in-memory map can stay as a hot L1, or be dropped (a single indexed row read is cheap).

### 4.6 Bottom line

- Do it for selections **and resolved links**; partition by `accountHash`; gate by `profileHash`; refcount (or stop-removing-on-teardown) the torrent lifecycle.
- The wins are **search elimination, shared links (fewer `requestdl`), restart survival, cross-user reuse, and never erasing useful entries** — not a big `createtorrent` cut (already ~deduped).
- Safety: (1) stop the `RemoveTorrent` teardown nuking shared items; (2) keep cached selections freely, bound uncached.

---

## Part 5 — Implementation status

**Landed + verified (build/vet/test green):**
- §2.2 L2 — TorBox 429 backoff in `doQueryCtx` (honor `Retry-After`, exp backoff, no jitter; body buffered so POSTs replay).
- §2.2 queue — `PrewarmStreams` drains via a repo `rate.Limiter` (no simultaneous burst).
- §2.2 C8 — dropped the 1s settle sleep; cached numeric file-id (no extra `getTorrent`).
- §2.1 S2 — `InitializeProvider` only wipes preloads on provider/account/key change.
- §2.4 (C12) — parser/HEAD cache TTL raised to URL lifetime (`metadataCacheTTL` 2h).
- §2.3 M1/M2 — metadata prewarm re-enabled for **tier-1 only** (`opts[0]`) + a directstream **CDN-warm limiter** (`cdnWarmLimiter`) so it can't 429 the CDN.
- **Part 4 shared DB (core)** — `DebridPrewarm` table (account+content+profile keyed), persist on resolve/refresh, **validated hydrate front-gate** on the prewarm path (CDN link-probe → re-resolve-from-itemId → drop-row-if-item-gone, C7), stop-`RemoveTorrent`-on-teardown (4.4 lazy-correct), TTL sweeper on the tick. `profileHashFor` gate unit-tested.
- **Part 4 play-path hydrate** — `startStream` now also hydrates from the shared DB on an in-memory miss (cross-user reuse / post-restart when no prewarm ran), **auto-select only** (a manual pick never reuses a shared row), with a **fast-fail probe** (`prewarmProbeTimeoutPlay` 2s) so a dead link falls through to a cold resolve instead of stalling the click; a live link validates in <1s.
- **§2.1 just-aired axis (S1/C3/C18)** — `selectPrewarmTargets` now picks from two axes: the 3 most-recently-watched in-progress shows **plus** up to 3 currently-RELEASING shows whose latest episode just dropped (`progress+1 == epCount` — a fresh episode, detected with no air-time math). `progress` is episode-selection only, never a ranking filter. Tier-1 (metadata) stays `opts[0]` = most-recent watched. Both axes unit-tested (`TestSelectPrewarmTargetsJustAired`). **A2 dropped** (never implemented). The "~1h-post-air" timing is handled implicitly: a too-early prewarm finds no torrents, caches nothing, and the 10-min tick retries until they appear (vs precise air-time scheduling).

**Staged (next, tracked):**
- §2.1 remainder — **S5** local-file filter (low value for streaming-only deploy + needs per-user local lookup), **A3** sequel prewarm (low-confidence, needs `FetchMediaTree`), **event-driven loop** (replace the 10-min poll with warm-on-login/progress-change — the 10-min poll works; this is an efficiency refactor).
- §2.6 watch-history recency bug — separate trace.
- L4 createtorrent budget counter; M3 structure-only parse; A4 FileStream seed.

**Deferred — explore later (deliberate, low priority):**
- **Full torrent reference-counting** (Part 4.4 ideal). We currently use the lazy-correct variant: stop removing torrents on teardown and let TorBox hold them (cached torrents are free — they don't count against the active-slot cap; the sweeper only reclaims DB rows). The ceiling is unbounded growth of the account's torrent list (benign for cached content; only cost is a larger `mylist` response, capped at 500 in `GetTorrents`). Upgrade to refcount-by-`(accountHash, torrentItemId)` across live streams + unexpired rows, deleting only at count 0, **if** account torrent-count growth ever becomes a real perf/limit problem. Needs a `torrent_item_id` column + cross-row/stream counting.
- **Unify `DebridActiveStream` into `DebridPrewarm`.** Two overlapping persistence tables coexist: per-user last-active-stream (restart reconnect) and the account-shared prewarm cache. An active stream is just another resolved selection+URL, so it could fold into the one shared table (tagged with the user) — the "single server-wide DB" ideal. Pure refactor with no new user value, so deferred; revisit when touching the persistence layer next.

> Not committed or deployed — build-only. Memory not yet updated (changes uncommitted).

---

## Part 6 — Review comments (resolved)

The inline `***…***` notes have been folded into the plan above and removed. Recorded here with their resolution.

| # | Section | Comment | Resolution |
|---|---|---|---|
| C1 | §1.1 | Why every 10 min, not the TTL? Why watch "watch activity" at all? | Tick is target-discovery, idempotent for warm entries (no re-resolve). It exists to warm the continue-watching row in the **cold case**; active watching is handled by the @3s client preload. → make it **event-driven** (§2.1). |
| C2 | §1.1 | Watch-history recency is broken; prewarm misses the right shows. | Real bug; likely the `CURRENT`/`REPEATING` filter or non-updating playback paths. **Tracked separately** (§2.6), deferred. |
| C3 | §1.1 | Add just-aired episodes alongside last-3-watched. | Adopted — new **just-aired axis** in revised S1 (§2.1). |
| C4 | §1.2 | Concurrent fan-out may cause 429; queue it. | Adopted — **serialized queue** (§2.2). |
| C5 | §1.3 | Is the mylist cache shared across users on the same key? | **Yes** — singleton TorBox client → shared 120s cache + append-on-add (documented §1.3). |
| C6 | §1.3 | Is polling a rate-limit source? | Contributor under the 300/min bucket, but `createtorrent` 60/hr binds first (§1.3, §2.5). |
| C7 | §1.3 | If the link is valid, no API call needed — probe it, recall only if dead. | Adopted — **link-validity probe** replaces blind time-based re-resolve (§2.4). |
| C8 | §1.4 | Is the 1s sleep + extra getTorrent needed? | No — **drop the sleep** (verify no 404) and **cache the numeric file-id** (§2.2). |
| C9 | §1.5 | Entry shouldn't be consumed — keep until TTL. | Adopted — keep-until-TTL (§2.4). |
| C10 | §1.5 | Link valid after episode ends — keep until TTL. | Adopted — same (§2.4). |
| C11 | §1.6 | These TTLs cover only the next episode, one per show? | **Confirmed yes** (§1.1, §1.6). |
| C12 | §1.6 | Why 15m parser TTL? Should match link lifetime. | Adopted — raise to URL lifetime (§2.4). |
| C13 | §1.6 | Don't delete a speculative entry that's already cached on TorBox. | Adopted — persist-and-reuse (§2.4). |
| C14 | §1.8 | Store metadata once, serve instantly on hit, overlap buffer→debrid. | Adopted — M1/M2 + clean handoff (§2.3); store-once folds into §2.4. |
| C15 | §1.10 | @3s next-ep: if cached/in-DB, call once. | Adopted — DB front-gate (§2.4, S4). |
| C16 | §1.10 | Cancel hover prewarm on unhover. | Mostly handled — grid hover is a no-op; hero hover debounces + cancels pending timers (§1.10). |
| C17 | §1.11 | No discard — store good links in DB. | Adopted — S2 + persist-and-reuse (§2.1, §2.4). |
| C18 | §2.1 S1 | Progress irrelevant; use just-aired; schedule ~1h after air. | Adopted — progress → episode-selection only; just-aired axis + 1h-post-air timing (§2.1). |
| C19 | §2.1 S4 | Drop hover; DB-check instead of full pipeline. | Grid hover already dropped; **DB front-gate** added (§2.1, §2.4). |
| C20 | §2.2 L1 | Use 59/299, don't waste budget. | Adopted with a small safety margin (~58/295) for counter/window skew + retry headroom (§2.2). |
| C21 | §2.2 L2 | Jitter does nothing against 429. | Agreed — **no jitter**; the queue removes the lockstep-retry case (§2.2). |
| C22 | §2.2 L3 | Queue instead of jitter. | Adopted — queue replaces semaphore+jitter (§2.2). |
| C23 | §2.4 A2 | N+2 look-ahead not needed. | Agreed — **A2 dropped**. |

---

## Sources

- [TorBox — API Rate Limits](https://support.torbox.app/en/articles/13726368-api-rate-limits)
- [TorBox — The Abuse System](https://support.torbox.app/en/articles/10336778-the-torbox-abuse-system)
- [TorBox — Account Restrictions](https://support.torbox.app/en/articles/9836418-account-restrictions)
- [TorBox — Can I use my account with many IPs? (no IP tracking)](https://support.torbox.app/en/articles/9836406-can-i-use-my-account-with-many-different-ip-s)
- [TorBox — Main API docs](https://api-docs.torbox.app/)
- [TorBox media-center #67 — startup re-fetch causing 429 flooding](https://github.com/TorBox-App/torbox-media-center/issues/67)
- [DeepWiki — DMM Debrid Services Integration (global limiter, backoff)](https://deepwiki.com/debridmediamanager/debrid-media-manager/6.1-debrid-services-integration)
- [comet-uncached — Auto Cache Next / advanced binge](https://github.com/Zaarrg/comet-uncached)
- [AutoStream — episode preloading](https://stremio-addons.net/addons/autostream)
- [Viren070 Guides — Stremio technical details (shared cache DBs)](https://guides.viren070.me/stremio/technical-details)
- [Netflix — ML for streaming quality (predictive caching)](https://netflixtechblog.com/using-machine-learning-to-improve-streaming-quality-at-netflix-9651263ef09f)
- [How Netflix designs for binge-watching (5-stage pipeline)](https://medium.com/@ismailkovvuru/how-netflix-designs-systems-for-binge-watching-system-design-explained-d1a6ffbc4e29)
- [Fastly — Video cache prefetch (thundering herd)](https://www.fastly.com/blog/video-cache-prefetch-with-compute-edge)
- [Network-aware prefetching for short-form video (arXiv 2209.02927)](https://arxiv.org/pdf/2209.02927)
