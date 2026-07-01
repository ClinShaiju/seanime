# Fable Audit — full fork review (2026-07-01)

> **FIX PASS (2026-07-01, same day):** the actionable findings below were implemented in the
> commit series `87385960..HEAD` (see §11 status table at the end for the finding → commit map).
> Everything is code-complete, built, vetted, and unit/race-tested; **live/manual testing is
> deliberately deferred** per instruction. Items NOT fixed are marked in §11 with why.

**Scope:** all 131 commits since fork point `315c8c9e`, plus the **uncommitted working tree**, cross-referenced
against every Claude Code conversation for this project (June 18 – July 1) and the prior audits
(`fork-audit.md` 2026-06-24, `nakama-audit.md`, `prewarm-audit.md`).

**Method:**
1. Mined all ~37 conversation transcripts (740+ user messages) for reported issues — especially recurring
   ones and ones reported fixed-then-broken. **Caveat honored (per your instruction):** your in-session
   confirmations/denials are treated as *observations*, not ground truth; findings flag where a "fix" was
   only ever validated by eyeball.
2. Verified `fork-audit.md`'s implemented fixes still hold on today's HEAD (they do — §6).
3. Deep read of the 26 commits **after** the previous audit (`e19c8845..HEAD`): nakama sync round 2,
   prewarm shared DB + fire badges, Honzuki season match, the read-ahead saga. No prior audit covered these.
4. Audit of the uncommitted working tree (`cdngate.go`, `httprs.go`, `httpstream.go`, `manager.go`).
5. Live checks: VPS runs **3.8.12** (binary dated Jun 26 06:48, service active).

Severity: **[H]** crash / leak / data-at-risk · **[M]** wrong results in a common case · **[L]** brittle /
cosmetic · **[U]** user-reported, still unresolved · **[+]** positive.

---

## 0. Status ledger — every recurring user-reported issue vs. code reality

| Issue (as you reported it) | Code reality today | Status |
|---|---|---|
| "sub/audio track data not getting sent" (recurred Jun 23, 25, 26) | **Root-caused**: `httprs` handed a CDN **429 error body to the MKV parser as if it were the file** → silent 0-track parse, then the poisoned parser was **cached for 2h** so it stayed broken across replays. Fix (reject non-2xx + retry + never cache 0-track) exists — but is **uncommitted and NOT deployed** (§1) | ⚠️ fixed in tree only |
| "Media format not supported" random error (Jun 27) | Same root class — garbage/throttled CDN response reaching the player path. Same uncommitted fix | ⚠️ fixed in tree only |
| CDN 429s mid-play / "cdn error but manual download works" (Jun 26) | Per-token connection gate (`cdngate.go`) bounds the stream-start burst that trips TorBox's per-link throttle. **Uncommitted, not deployed** | ⚠️ fixed in tree only |
| TorBox API 429s blamed on prewarm (Jun 22–27) | Rate-safety landed in `db1a4b59` (429 backoff + serialized prewarm queue + CDN-warm limiter) and **is deployed** (3.8.12). Note: your attribution of 429s to prewarm was partly wrong — the per-link CDN burst at stream start (previous row) was a separate, unaddressed source until the uncommitted work | ✅ deployed / ⚠️ partial |
| Watch-room rubberbanding when a follower stalls (Jun 26) | `703e010c` (server) + web + Tenji `9f8656d`: never rewind to a stalled driver, buffering-hold, seek cooldown | ✅ in code — **never live-tested** (§2.9) |
| Follower closing player killed host's stream (Jun 26) | Host-only stop, server-enforced + client emit gating | ✅ in code — never live-tested |
| Play/pause inversion, seek not propagating (Jun 23–25) | Server-authoritative state + sender exclusion + echo debounce + apply-unconditionally fix. Your last test (Jun 26 01:36) predates the final fixes | ✅ in code — never live-tested |
| Same-account two-device room sync silently broken | Participants still keyed by `PoolUser.Key()` = username → one slot per account. Known, deliberate low-pri | 🔴 **open** |
| iOS still shows "server stream the episode still active" (Jun 26) | No fix landed anywhere I can find | 🔴 **open** |
| "Join room stream button doesn't show up unless you rejoin" (Jun 24) | Partially fixed (`0507077b` broadcasts room state on start/stop). But a **non-host driver who closes their own player still can't see the button** — new finding §2.2 | 🟡 partial |
| Tenji stuck on "selecting torrent" (Jun 22) | Root cause is upstream AIOStreams aggregation tail (~10s AnimeTosho); the config levers (dynamic fetching exit-condition, AnimeTosho timeout cap) from fork-audit §9 are **server-config items — no evidence they were ever applied on the VPS** | 🟡 open (config) |
| Rascal dual-audio "suddenly worked, not sure what changed" (Jun 24) | Almost certainly the 429→0-track bug above: the tracks were always in the file; the parse was throttled. Your observation was right, the then-current explanation ("429 maybe") was half-right, and no durable fix shipped until the uncommitted tree | ⚠️ explained now |
| Honzuki/Ascendance wrong-cour selection (Jun 24) | `66f21c9f` title-first season + year guard, tested. But the year guard has a **batch regression risk** (§4.1) | ✅ + watch item |
| Denshi onboarding has no external-server option (Jun 26) | No commit addresses it; only the temp workaround from that session | 🔴 **open** |
| Trakt "proper integration" (requested Jun 20) | Never started (no commits, no writeup) | 🔴 open (never started) |
| Denshi buffering on CDN dips | Read-ahead: first attempt reverted; single-connection version `77b57779` is **default-OFF behind `SEANIME_DIRECTSTREAM_READAHEAD`** — so production behavior is unchanged unless you set the env var in the systemd unit (I did not find evidence it's set) | 🟡 built, not enabled |
| Stale-tab reload prompt on Denshi | Deliberately reverted to browser-only (`8dc7eac5`) — correct call (Denshi loads bundled JS) | ✅ by design |

---

## 1. Uncommitted working tree — the CDN gate / 429 / 0-track fix

Files: `internal/directstream/{cdngate.go(new), httpstream.go, manager.go}`,
`internal/util/http/{httprs.go, httprs_status_test.go(new)}`.

### 1.0 [H-process] The fix for your #1 recurring bug is sitting undeployed
This work root-causes and fixes the **most-reported bug family in the entire transcript history**
(missing audio/sub tracks, "Media format not supported", mid-play CDN failures). The VPS binary is from
Jun 26 and does not contain it. `seanime-test.exe` in the repo root suggests it was built and locally
smoke-tested, then the session ended. **Commit + deploy this first.** It's also the prerequisite for
trusting any future watch-room test (a follower's metadata parse hitting a 429 produces exactly the
"player opens, nothing plays" symptom you spent days chasing).

### 1.1 [+] The root-cause fix itself is correct and well-tested
- `httprs.NewHttpReadSeekerFromURLWithHeaders` and `makeRangeRequest` previously **swallowed non-2xx
  responses and served the error body as file content**. This is a **pre-fork upstream bug** — every
  0-track parse, silent track loss, and at least some "format not supported" errors flow from it.
  The `StatusError` + retry + reject design is right, and `httprs_status_test.go` covers both paths.
- The 0-track poison guards (never cache a track-less parser; degrade playback instead of hard-failing)
  close the 2h-poisoned-cache loop.
- The per-token gate correctly holds one slot across the whole retry loop and releases exactly once.

### 1.2 [M] `warmStreamStart` can park forever and then steal a slot from a live seek
`manager.go:245` acquires the gate with `context.Background()`. If the user is *playing that very link*
(the exact collision the comment describes), both slots can be held by long-lived serve connections and
the warm goroutine **parks indefinitely**. Worse: it sits in the semaphore queue, so when the player
aborts a range to seek, the freed slot is raffled between the user's new range request and the parked
16–48MB warm read — Go `select` is random, so ~half the time **the user's seek waits behind the warm
read**. Semantically, a contended token means the link is already warm. Fix: non-blocking try-acquire
(`select` with `default`) and skip the warm when the token is busy.

### 1.3 [M] `httprs` retry sleeps are uncancellable and the comment understates the worst case
`makeRangeRequest` retries with `time.Sleep` up to 3 times; with `Retry-After` honored the total block is
up to **15s** (3×5s), not "at most one 3s backoff" as the ponytail comment claims — and it runs under the
read path with no context, so a user closing the player waits it out. Bounded, so acceptable short-term,
but fix the comment and consider threading `hrs.ctx` when you next touch this file. Related pre-fork gap:
the underlying client has **no read deadline**, so a CDN that stalls mid-body (not erroring) still hangs
the metadata parse indefinitely → "watch" never fires (the old follower-never-plays signature).
The retries fix the *error* case, not the *stall* case.

### 1.4 [M-verify] Gate key vs. real TorBox URL shape — two opposite risks
`cdnTokenKey` strips only the query string. Verify what per-peer re-resolved links for the same file look
like:
- If the **path is stable** and only `?token=` varies (what the comment assumes): a watch room with
  **3+ members on the same file funnels every member's long-lived serve connection through one 2-slot
  gate → the 3rd member's stream blocks**, which would present as exactly the "follower opens, buffers
  forever" symptom. The gate cap must be ≥ concurrent consumers of a shared file, or the long-lived serve
  should be exempted (gate only the bursty short reads: metadata, warm, tail probes).
- If the **path varies per resolve**: cross-resolve dedup is lost, but the gate still fixes the
  single-player start burst (same URL), which was the observed problem. Fine.
One `journalctl` grep of two peers' resolved URLs answers this. Do it before a 3-person room test.

### 1.5 [L] Smaller items
- `PrewarmStreamMetadata` burns a `cdnWarmLimiter` token **and** does the HEAD before the
  `parserCache` already-cached check — reorder so a cache hit is free.
- `probeStreamURL` (committed, `prewarm_db.go`) treats a 429 as "dead link" → triggers an unnecessary
  `requestdl` re-resolve under throttle, and it doesn't go through the new gate. Align it: 429 = alive.
- Codegen emits **unexported** Go fields (`lastDiscreteAt/By`) into `types.ts` — harmless but noise;
  commit the regenerated files with the Go change or teach codegen to skip unexported fields.

### 1.6 [H] Repo hygiene: secrets one `git add -A` away from a public repo
`_vps-backup/` (contains `seanime.db.bak` — bcrypt hashes, session tokens, AniList tokens, debrid API
keys — and `config.toml.bak` — the **server password in plaintext**), `_test-data/`, `seanime-test.exe`,
`seanime-test.exe~`, and `deploy/` are untracked and **not in any ignore file**. The fork is **public**.
Add `_vps-backup/`, `_test-data/`, `*.exe`, `*.exe~` to `.git/info/exclude` (or move the backups out of
the repo entirely). `deploy/` looks intentional (auto-update service files) — decide and either commit
or ignore it.

---

## 2. Nakama watch rooms — round 2 (post-audit commits)

The round-2 stack (server-authoritative + echo debounce `87d4b592` + buffering guards `fa616588`/`525aa68e`
+ handoff `dc17a5f0` + no-rewind/host-only-stop `703e010c`, with Tenji parity through `9f8656d`) is
**architecturally coherent** — genuinely good: sender-excluded broadcasts, per-leg RTT compensation
(each client leads by only the half-RTT it can measure), playbackRate glide with deadband, stall-as-pause
reporting, discrete-vs-heartbeat separation. The prior audit's crash fix (snapshot marshaling) and reaper
are intact. Remaining findings:

### 2.1 [M] Control handoff is a theft vector for buffering clients
`watch_room.go:657`: **any** genuine-looking discrete action from a `CanControl` member hands them
control. The guards (crossEcho 600ms, flipChatter 500ms, client-side stall-emit suppression) are
heuristics; a stalled MPV emitting a pause >600ms after the last change is indistinguishable from a user
pause → the stalled client **becomes the driver** and the room anchors to its frozen position. The web/
Tenji no-rewind logic protects followers from a stalled driver's *position*, but a stolen-then-paused room
still pauses everyone. Consider: only hand off control on **seek or play** (a bare pause updates state
without stealing the controller), or require two discrete actions within a window to transfer.

### 2.2 [M] A non-host driver who closes their player is wedged out of the Join button
Close → client sets `optedOut` but the server still has them as `ControllerKey`. `useRoomStreamJoin.canJoin`
excludes `amController` → **no Join button** until someone else performs a discrete action (handoff).
Meanwhile nobody heartbeats, so the room free-runs on wall clock. Fix: on a follower/driver close, emit a
control-relinquish (or have the server demote `ControllerKey` to the host when that participant's stream
ends), and/or drop the `!amController` condition when the controller has no active player.
This is very likely the residue of your Jun 24 "join button doesn't show up unless you rejoin the room".

### 2.3 [L] Host-reconnect echo window
`JoinRoom` restores `ControllerKey` to a returning host but leaves `lastControllerClientID` pointing at
the previous driver — for up to ~1s (until the host's first heartbeat) the ticker echoes stale position
*to the host*, which now applies syncs unconditionally. Transient one-second twitch after host reconnect;
set `lastControllerClientID` to the host's new client on reclaim.

### 2.4 [L] `NAKAMA_ROOM_DEBUG` diagnostics still shipped (3rd deferral)
Web sends hook-state + recv-gate + apply lines; Tenji mirrors; server logs them. Every prior audit
deferred stripping "until playback is confirmed live". Given §2.9, keep them for ONE more verified test,
then strip in all three repos in the same change.

### 2.5 [L] Room cards broadcast to everyone, including pre-login clients
`broadcastRoomsUpdated` uses global `SendEvent`, so anonymous (server-password-only) sockets on the
networked VPS receive room names, host usernames, and what media is being watched. Same class as the
fork-audit §3 anon-leak; route through the scoped path or accept it as by-design pool visibility.

### 2.6 [U] Same-account multi-device collision — still open (known, deliberate).
### 2.7 [U] iOS "server stream still active" after room close — no fix landed; untriaged.
### 2.8 [L] `SetControl` broadcasts via `go` while siblings broadcast synchronously — harmless inconsistency.

### 2.9 [M-verify] The entire round-2 stack has never been live-tested
Your last recorded cross-platform test is **Jun 26 01:36**, before `703e010c` (server), before Tenji
`9f8656d`, and before the uncommitted CDN fixes that remove a major confounder (a follower's 429'd
metadata parse looks identical to a sync bug). Next test, in order: deploy §1 → verify §1.4's URL-shape
question → then run the td/dt matrix once. Also confirm a Tenji build newer than `9f8656d` is actually
installed — the Windows `eas update` hermesc crash means OTA may not have gone out (build via GH Actions
if in doubt).

---

## 3. Prewarm shared DB + fire badge + metadata warm (`db1a4b59`, `c68e31fe`)

Solid implementation, faithful to `prewarm-audit.md`'s plan: account+profile-keyed rows, probe-then-
re-resolve-then-drop hydrate ladder, play-path fast-fail probe (2s), TTL sweeper, serialized prewarm
queue, 429 backoff with replayable POST bodies, tier-1-only metadata warm behind the CDN limiter.
`persistPrewarm` correctly writes on both resolve and URL refresh. Findings:

- **[L/M] The red "metadata" badge overstates.** `GetPrewarmStatus` reports `Metadata:true` from the
  DB row's original `Opts`, but the parser cache is **in-memory and per-user-Manager** — after a server
  restart, or for a *different* user reusing the shared row, the badge shows metadata-warm while the
  actual parse will happen at play time. Cosmetic, but it will mislead your own testing ("badge says hot,
  still saw Loading metadata"). Either check `parserCache` membership for the requesting user's manager,
  or accept and document the badge as "selection shared + metadata was warmed for someone".
- **[L] `probeStreamURL` 429-as-dead** (see §1.5) — this is the committed half of the same inconsistency.
- **[L] Doc/memory drift:** `prewarm-audit.md` Part 5 says metadata prewarm re-enabled (true —
  `prewarm.go:252` sets tier-1) but the project memory still said "OFF everywhere, dead code". Corrected
  in memory as part of this audit.
- **[+]** The quality-over-speed rule is mechanically enforced (`profileHashFor` gate, auto-select-only
  sharing, manual picks never reuse shared rows) — exactly per your standing constraint.

---

## 4. Autoselect — Honzuki fix (`66f21c9f`) + residual classes

- **[M] Year guard vs. multi-season batches.** The new wrong-cour guard buries any release whose parsed
  year differs from the entry's start year by >1. But you explicitly *prefer curated batches*, and
  complete-series batches are commonly labeled with the **premiere** year (e.g. "Show (2019) S1–S4
  Complete") — streaming a 2023 cour, that batch takes `-scoreSeasonMismatch` and loses to a worse
  single. BDs also lag past the ±1 tolerance. Recommendation: skip the year guard when the release parses
  as a batch/episode-range, or halve the penalty for batches. (Tests only cover the Honzuki single case.)
- **[L] Title-first flip is a convention bet.** `ResolveExpectedSeason` now trusts the AniList
  "…Season N" ordinal over TMDB metadata. Right for Honzuki-style lumped cours; wrong whenever scene
  groups label by TMDB convention while AniList's ordinal differs. Mixed-convention result sets will
  bias toward AniList-ordinal-labeled releases. Watch the known failure classes (Rascal S2, Wistoria S2,
  CotE S4, DanMachi S4 cours) on next occurrence rather than pre-fixing.
- **[+]** Prior fixes verified intact: dual-audio name regex, size-token stripping, episodic-format→TV,
  flag-language decode, `Identity()` dedup. Autoselect suite passes per the last audit run.

---

## 5. Directstream read-ahead saga (`57187933` → revert `3fb8e616` → `77b57779`)

The final single-connection design (producer fills the FileStream cache from the *player's own* CDN
response, consumer serves from cache, 96MiB window, incomplete-fill closes the reader so the player
re-requests) fixes exactly what killed the first attempt (a second diverging connection). Code reads
correct: offsets are 206-gated, producer/consumer share `fillOff/serveOff` atomics, `context.AfterFunc`
unblocks the consumer on disconnect.
**But it's default-OFF and I found no evidence `SEANIME_DIRECTSTREAM_READAHEAD=1` is set in the systemd
unit** — meaning the Denshi buffering behavior in production is still the plain live-tee. If the Jun 26
buffering complaints are to be re-tested meaningfully, decide: enable it on the VPS (one env line) or
accept the status quo. Note the interplay with §1.4: with read-ahead ON, the serve connection is held
longer per token — same gate-cap question applies.

---

## 6. fork-audit.md fixes — verified still on HEAD

Spot-checked every "FIXED" row: merged-season per-user collection (`anime_franchise.go:244`),
`dualAudioNameRe` (`comparison.go:19,317`), `StreamManager` `stateMu`/`prevOptsMu` guards, ws
`requireUserScoping` anon-skip (`websocket.go:235`), franchise-group cache error gate
(`anime_franchise.go:196-200`), room `Snapshot()` marshaling + idle reaper, `systemUserID` admin plane
(`modules.go:49`). **All present.** No regressions found in the round-2 commits against these.

---

## 7. Pre-fork (upstream) issues that interact with fork code

1. **`httprs` non-2xx swallow** — upstream bug, root of the track-loss family; fixed in your uncommitted
   tree (§1). Consider PRing upstream once proven.
2. **No read deadline on CDN readers** — a mid-body stall (not an error) still hangs `GetMetadata`
   indefinitely; `manager.playbackCtx` has no per-operation timeout. This is the remaining way a follower
   can sit on "Loading metadata…" forever. Cheap fix: wrap the metadata parse in a
   `context.WithTimeout(playbackCtx, ~30s)`.
3. **videocore clientId pinning (#814 class)** — `videocore.go:880` filter vs. ws reconnect clientId
   drift; `denshi-tracking-check.md`'s verification checklist was never ticked off. If Denshi progress
   tracking ever "stops" again, that checklist is the diagnostic.
4. **`TestDownlaoded_KeepItemOnDownloadUrlFailure` nil-map panic** (`repository_test.go`) — pre-existing,
   still unfixed; one-line init in the test's struct literal. It makes `go test ./internal/debrid/...`
   red, which trains you to ignore test failures — worth the one line.
5. **Thumbnail requests** read from the FileStream cache at arbitrary offsets; unchanged upstream
   behavior, but note they bypass the new token gate (they don't hit the CDN directly — fine).

---

## 8. Consolidated recommendations (priority order)

1. **Ignore/relocate `_vps-backup/`** and the test binaries (§1.6) — 2 minutes, closes a real
   secret-leak path on a public repo.
2. **Fix the logger race** (§9.1, one line) — a live corruption/crash risk in every deployment, and it
   unblocks `-race` for everything else.
3. **Fix the cross-stream cancel race** (§9.2) — silent "second episode never starts" class.
4. **Commit + deploy the working tree** (§1.0) after fixing §1.2's try-acquire (small) — it resolves the
   top recurring bug family and de-confounds all future room testing.
5. **Rate limits** (§10.4): tag 429 origins, buffer the metadata parse to ≤2 CDN requests, per-limiter
   `requestdl` (~20/min effective), make the prewarm drain truly serial (§9.5), separate/account the
   AIOStreams key spend.
6. **Answer §1.4 (TorBox URL shape)** with one log grep; adjust the gate cap/exemption if paths are stable.
7. **Run the one deferred live room test** (§2.9), then strip `NAKAMA_ROOM_DEBUG` everywhere (§2.4).
8. **Fix the non-host-driver Join-button wedge** (§2.2), the handoff-on-pause theft vector (§2.1), and
   the join-stream different-release fallback (§9.4) — the three most likely causes of your *next*
   "sync broke again" report.
9. **Soften the year guard for batches** (§4.1) before it eats a SeaDex complete batch on a later cour.
10. Decide on read-ahead in prod (§5): set the env var or park the feature.
11. Small cleanups batch: probe-429-alive (§1.5/§3), metadata-badge accuracy (§3), prewarm cache-check
    order (§1.5), host-reconnect echo (§2.3), metadata parse timeout (§7.2), repository_test nil map
    (§7.4), updater test compile (§9.3), login throttle + role validation + share-triple clear (§9.6).
12. Open features never started, for the backlog: Trakt integration, Denshi onboarding external-server
    field, iOS "stream still active" indicator, AIOStreams config levers on the VPS (fork-audit §9 [1]–[5]).

---

## 9. Independent findings — second pass (not derived from your reports)

Cold read of `stream.go`, `filestream.go`, `prewarm.go`, `user_auth.go`, `nakama_rooms.go`,
`logger.go`, plus `go build`/`go vet`/`go test -race` runs across the backend.

### 9.1 [H] Production logger data race — confirmed by the race detector
`internal/util/logger.go:36,66`: the file-output `ConsoleWriter` writes into the package-global
`logBuffer bytes.Buffer` from **every logging goroutine with no lock** — `logBufferMutex` only guards
the flush. `go test -race ./internal/torrents/autoselect/` fails today with 10 DATA RACE warnings on
exactly this buffer (two concurrent search goroutines logging). Concurrent `bytes.Buffer` writes are
undefined behavior: interleaved/corrupted log lines at best, a panic inside `Buffer.grow` at worst —
in the *server process*, on every deployment. Pre-fork upstream code, but the fork's added concurrency
(parallel batch+single search `81832704`, prewarm goroutines, room broadcast loop, per-user sessions)
turned a cold path hot. **Fix is one line:** wrap the writer (`Out: zerolog.SyncWriter(&logBuffer)`).
Side benefit: the `-race` flag becomes usable for the whole repo (see 9.3).

### 9.2 [H/M] Cross-stream cancellation race in `StreamManager`
`stream.go:530-536` (and the playback-subscriber twin at `:763-768`): the download goroutine's deferred
cleanup cancels via the **shared field** `s.downloadCtxCancelFunc`, not its own local `cancelCtx`.
Sequence: episode B starts → cancels episode A's ctx (`:337`) → assigns the field to B's cancel
(`:513`, ~0.5–2s later, after AddTorrent) → episode A's goroutine finally notices its cancelled ctx
(its poll backoff is up to **4s**) and its defer fires — **cancelling episode B's context**. B's poll
loop then dies at the silent `ctx.Err()` check (`:580`), no `AbortOpen` is sent, and the user sits on
"Downloading torrent…" forever. This is a plausible self-inflicted cause of the sporadic
"second episode start hangs / had to press again" behavior that was never attributed. Fix: the defer
must capture and cancel **its own** `cancelCtx`, and only CAS-nil the field if it still points at it.

### 9.3 [M] `go test -race` is red; `internal/updater` tests don't compile at all
- `TestSearchFromProvider_BatchFallback` fails under `-race` (9.1's buffer). Until fixed, the race
  detector — the tool that would have caught the watch-room map crash before production — is unusable.
- `internal/updater/{check,updater,test_helpers}_test.go` reference `websiteUrl`, deleted by
  `e19c8845`'s update-pipeline rework → the package's tests **haven't compiled since 3.8.9**.
  (`go vet ./internal/...` also surfaces it.)

### 9.4 [M] Watch-room join-stream fallback can put a follower on a different release
`nakama_rooms.go:258-262`: when the controller's share isn't captured yet (`GetUserStreamShare` miss —
exactly the race where a follower reacts to the first sync while the host is still resolving), the
handler silently falls back to `AutoSelect=true`. The follower then runs its own selection and can land
on a **different release** than the host (different intro timings, different duration) → the position
sync is permanently "off" in a way no amount of sync tuning fixes, plus a full search + extra TorBox
calls per late joiner. Fix: brief retry of the share (the host resolve completes within seconds) before
falling back; or fail with "stream not ready yet" and let the Join button retry.

### 9.5 [M] The prewarm "serialized queue" is a stagger, not a serialization
`prewarm.go` (client) `:26-42`: the drain goroutine spaces *kickoffs* 1.5s apart, but
`preloadStream` **spawns its own goroutine and returns immediately** — so resolves still overlap.
With ~10s AIOStreams searches and up to **6 targets/user/tick** (3 watched + 3 just-aired — note: not
the 3 the docs/memory say), a 2-user tick can still have ~8 resolves in flight, each firing its
mylist/requestdl cluster when its search completes. The comment ("no longer hits TorBox
simultaneously") overstates. Fix: make the drain wait for completion (run the resolve synchronously in
the drain goroutine) — the whole point of a background queue is that latency there is free.

### 9.6 [L] Smaller self-found items
- **`/user/login` has no brute-force throttle** — bcrypt is the only speed bump; behind the server
  password, but that's also unthrottled. A tiny per-IP/username attempt limiter closes it.
- **`cancelStream` clears only `currentStreamUrl`**, leaving `currentTorrentItemId`/`currentFileId`
  stale — the share snapshot can expose a stale selection with an empty URL. Harmless today
  (join-stream checks the URL path), a footgun tomorrow. Clear the triple together.
- **Cold-started episodes never release their parser cache**: `DropStreamMetadata` fires only via
  preload-entry bookkeeping (`lastConsumedKey`), so an episode started without a preload keeps its
  fonts (tens of MB) in RAM the full 2h TTL. Bounded, but track the active URL instead.
- **Direct-URL (`StreamUrl`) preloads can't refresh**: no `torrentItemId` → the `urlRefreshTTL` path
  is skipped, and the in-memory hit path has no probe — a >2h-old direct-URL preload plays a dead
  link. (DB-hydrate path *does* probe; the in-memory path doesn't.)
- **`HandleUserRegister` stores an arbitrary `role` string** — a typo'd role silently makes a
  non-admin; validate against the two constants.
- **`fileStreamReader.Read` busy-polls at 10ms** while waiting for pieces (pre-fork; read-ahead makes
  it the steady-state consumer path — a cond-var/broadcast would be cleaner if read-ahead is enabled).
- **updater vet noise**: `discordrpc` non-constant format strings, transcoder non-deferred
  `time.Since`, `scan_logger` copies a `sync.Mutex` — all pre-fork upstream, all benign, listed for
  completeness.

---

## 10. Rate limits — what the implementation is actually doing (the misunderstanding)

You're right that by the *documented* budgets (300/min/endpoint, 60/hr createtorrent) the deployment
should never 429. It does anyway because **three assumptions in our model are wrong**:

### 10.1 The 429s you see are mostly NOT the TorBox API — they're the CDN edge
Every fix so far (backoff in `doQueryCtx`, the prewarm queue, the createtorrent accounting) meters
**API** calls. But the Jun 26–27 429s were on **CDN GETs** (metadata reads, video ranges — `tb-cdn`/
storage hosts), which have their own undocumented per-link / per-IP burst throttling, unrelated to the
per-key API budget. "Manual download works fine" was the tell: one connection from your home IP vs.
the VPS making a burst from one IP. Nothing in the codebase measured or limited this until the
uncommitted `cdngate.go`.

### 10.2 The CDN burst is structural: one play = a storm of requests on one link
Traced mechanics (`httprs.go`): **every `Seek` closes the response body and the next `Read` opens a
brand-new HTTP range request** (`Seek` at `:189-199` nils `resp`; `Read` re-requests). An MKV metadata
parse seeks through header → tracks → attachments (per font) → cues at the file tail → back, so **one
"Loading metadata" step is ~5–15 rapid sequential GETs on the same link**, plus:
- the constructor's initial no-Range GET (starts a full-file download that's discarded on first seek),
- the HEAD (`FetchStreamInfoWithHeaders`),
- the player's `bytes=0-` main range + its tail-index probe,
- `warmStreamStart`'s 16–48MB range (tier-1 prewarm),
- `probeStreamURL`'s 1-byte GETs (every DB-hydrate attempt: each cold click + each prewarm-tick
  front-gate),
— all against **one link, from one VPS IP, within seconds**, multiplied by concurrent users and
watch-room peers. The uncommitted per-token gate caps *concurrency* at 2 but does **not** space
*sequential* requests, so if the CDN throttles on request rate (likely, given 429s with `Retry-After`),
the parser's seek storm can still trip it. **The real fix is cutting request count, not just
concurrency**: buffer the metadata read (MKV structure lives at the head + cues at the tail — a
2-range fetch into memory serves the entire parse: ~15 requests → 2).

### 10.3 The API key budget is shared with services Seanime can't see
The same TorBox account key is configured in **AIOStreams** (and via it, upstream addons — your own
test links show `addon.debridio.com/...torbox/<key-hash>/...`). Every search Seanime runs — every
manual browse, every auto-select, every prewarm re-resolve — fans out through the aggregator, whose
debrid integrations make their **own** TorBox API calls (`checkcached` etc.) against the same key.
Seanime's rate-safety meters only Seanime's calls; the real budget is "300/min minus whatever the
aggregator stack spends", and a prewarm tick *triggers* aggregator spend it doesn't count.
Two more corrections to the model:
- Community reports place **`/requestdl` at roughly ~20/min** of effective throttle (a Jellyfin+rclone
  user hit 429s even pacing at 1/s), far below the assumed 300/min. Seanime calls `requestdl` on every
  play, URL refresh, dead-probe re-resolve, and watch-room peer join.
- Creation-class endpoints carry a **10/min edge** on top of 60/hr (per TorBox's docs for
  `createwebdownload`), so a burst of 10+ adds in one minute 429s even with hourly budget to spare.

### 10.4 What to change (ordered)
1. **Classify before fixing**: tag every 429 log line with its origin (`api:<endpoint>` vs
   `cdn:<host>`) — one field on the existing warn logs. All future blame becomes data.
2. **Cut the metadata parse to ≤2 CDN requests** (buffered head+tail reader). Biggest single reducer;
   also makes the parse immune to mid-parse throttles (the 0-track family).
3. Deploy the per-token gate (§1) with the `warmStreamStart` try-acquire fix.
4. **Treat `requestdl` as ~20/min**: put it behind its own small limiter, and stop calling it
   speculatively (probe-fail → re-resolve is the only legitimate speculative caller; the §1.5/§3
   "429-probe = dead" bug currently *manufactures* requestdl calls under throttle).
5. Make the prewarm drain truly serial (9.5).
6. Account for the aggregator: enable AIOStreams' own caching levers (fork-audit §9 [4]) so a Seanime
   search doesn't re-trigger upstream TorBox spend, and consider a second TorBox key for AIOStreams if
   the plan allows — separating the budgets makes both sides' accounting true.

Sources: [TorBox API rate limits](https://support.torbox.app/en/articles/13726368-api-rate-limits),
[TorBox main API docs](https://api-docs.torbox.app/),
[torbox-media-center #67 — startup re-fetch 429 flooding](https://github.com/TorBox-App/torbox-media-center/issues/67),
[AIOStreams #582 — TorBox limit error codes](https://github.com/Viren070/AIOStreams/issues/582),
[TorBox changelog (per-key synchronized limits, CDN routing)](https://feedback.torbox.app/changelog).

---

## Appendix — observation-vs-truth notes (per your caveat)

Cases where the transcript record and the code disagree, resolved in favor of the code:
- **"it suddenly worked" (Rascal dual-audio, Jun 24):** nothing changed server-side at that moment; the
  CDN throttle window simply passed. The bug was real and is the §1 root cause.
- **"prewarm is causing the 429s" (Jun 22–27):** partially — the prewarm *font fan-out* was one source
  (fixed via limiter), but the per-link stream-start burst was a second, independent source that
  persisted until the uncommitted gate. Both were "TorBox 429" to the eye.
- **"denshi shouldn't buffer, read the logs" (Jun 25):** Denshi buffering was real but its *cause*
  (plain `<video>` + live-tee, no read-ahead + CDN dips + seek-reset) is structural, not a regression —
  the rubberbanding it caused in rooms was the actual regression and is what got fixed.
- **"whatever you did fixed it" (auto-sort, Jun 20):** several of those confirmations coincided with
  cache expiry or provider variance; the ranking code kept changing for days afterward, which is why
  "fixed" reports recurred. The current ranking has unit tests pinning the known cases — trust those over
  spot checks.
---

## 11. Fix-pass status (2026-07-01) — finding → commit

All built + vetted; `go test -race` green across nakama / torrents / util/http / directstream /
events / updater; `tsgo` (web) and `tsc` (Tenji) clean. **Live testing deferred by request.**

| Finding | Status | Commit |
|---|---|---|
| §1.6 secrets/binaries unignored in public repo | ✅ `.git/info/exclude` (local, uncommittable by nature) | — |
| §9.1 logger data race (prod) | ✅ `lockedLogBuffer` under the flush mutex | 87385960 |
| §9.1-adjacent: shared search-result mutation + extension-cache swap races (found while fixing) | ✅ copy-on-aggregate + `atomic.Pointer` | 87385960 |
| §9.2 cross-stream cancel race | ✅ goroutines cancel own ctx; cancel funcs under `stateMu` | b1885ef3 |
| §9.6 share-triple clear / cold-episode parser RAM / stale direct-URL preload | ✅ | b1885ef3 |
| §1.1–1.5 CDN gate + non-2xx rejection + 0-track guards (was uncommitted) | ✅ committed; `warmStreamStart` now try-acquire (§1.2); probe-429-alive (§1.5); prewarm cache-check-first (§1.5); httprs comment corrected (§1.3) | 70d262c0 |
| §10.4-2 metadata parse request storm | ✅ `ChunkedHttpReadSeeker` (8MiB chunks, cached, retried) wired into metadata reads; unit-tested | 54140279 |
| §10.3 requestdl ~20/min | ✅ paced ~15/min burst 3 | c285a4fb |
| §10.4-1 429 origin classification | ✅ `api:<endpoint>` / `cdn:<host>` tags on 429 logs | c285a4fb |
| §9.5 prewarm "queue" only staggered | ✅ drain runs resolves synchronously | c285a4fb |
| §2.1 pause steals control | ✅ bare pause applies but never transfers controller; unit-tested | 86597516 |
| §9.4 join-stream release divergence | ✅ retries host share 8×750ms, then retryable error (no silent auto-select) | 86597516 |
| §2.3 host-reconnect echo | ✅ `lastControllerClientID` repointed on reclaim | 86597516 |
| §2.5 room cards to anon sockets | ✅ `SendEventToLoggedIn` (identical on local installs) | 86597516 |
| §2.2 Join-button wedge (non-host driver) | ✅ web + Tenji: dropped the controller exclusion (watchingThis already covers an active driver) | 86597516 / tenji |
| §4.1 year guard buries premiere-year batches | ✅ packs exempt (season gate still polices labeled batches — Honzuki test passes); new test | 22235bca |
| §9.3 updater tests don't compile | ✅ rewritten for the GitHub-only flow | 8760d144 |
| §7.4 repository_test nil map | ✅ field init in test literals (remaining failure on Windows is TempDir/sqlite file-lock cleanup, env-only) | 8760d144 |
| §9.6 login brute-force / role whitelist | ✅ 5-failure/30s lockout; role ∈ {admin,user} | 8760d144 |
| §7.2 metadata parse can hang forever | ✅ bounded 45s (play + prewarm paths) | 8760d144 |

**Deliberately NOT done (and why):**
- §2.4 `NAKAMA_ROOM_DEBUG` strip — still load-bearing for the one deferred live room test; strip
  server+web+Tenji together after it passes.
- §2.6 same-account multi-device rooms — design change (participant keying), known low-pri.
- §2.7 iOS "server stream still active" — needs live repro to triage; untouched.
- §3 metadata-badge accuracy — cosmetic; needs cross-module plumbing (parserCache is per-user-Manager).
- §5 read-ahead enablement — a VPS env-var decision, not code.
- §1.4 TorBox URL-shape check — needs live logs from two peers (one journalctl grep at next test).
- §10.4-6 AIOStreams caching levers / second key — deployed-instance config, not fork code.
- §9.6 filestream 10ms poll — pre-fork; only worth touching if read-ahead is enabled.
- Backlog features (Trakt, Denshi onboarding server field, movies/TV) — unchanged scope.
