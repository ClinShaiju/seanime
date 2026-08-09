# Seanime battle-test (dynamic) — 2026-07-16

Companion to [`all-features-2026-07-16.md`](./all-features-2026-07-16.md) (static half) and
[`audit-test-2026-07-16.md`](./audit-test-2026-07-16.md) (manual test guide).

**This doc is the DYNAMIC half those two explicitly lacked.** Quote from the test guide:
> "No server was started, no Denshi launched, no browser opened during the audit. Nothing here has been
> observed actually running."

Method: real execution — `go test -race` sweeps, headless local server, HTTP/WS API driving. No CDP unless
forced.

Status legend: **open** / **fixed** / **deferred**. Verdicts: CONFIRMED / PLAUSIBLE / REFUTED.

---

## 0. Harness corrections to the previous audit

### HARNESS-1 — "cgo unavailable → no `-race`" is **false** — the race detector works here

The prior audit doc claims:

> "**No `-race`** — cgo unavailable on the Windows dev host. The riskiest lock changes in the diff;
> reasoned about, not detector-proven."

**This is wrong, and it mattered.** MinGW-W64 (ucrt, gcc 15.2.0) is installed at
`C:/Users/Clin/Documents/Programs/mingw64/bin/gcc`. With it on `PATH` and `CGO_ENABLED=1`,
`go test -race` builds and runs fine on windows/amd64:

```bash
export PATH="/c/Users/Clin/Documents/Programs/mingw64/bin:$PATH"
export CGO_ENABLED=1
go test -race -count=1 -p 1 -timeout 1200s ./internal/...
```

**Consequence:** the two fixes the guide called "the riskiest lock changes in the diff, reasoned about, not
detector-proven" (TS-1, PLAYLIST-1) *were* provable all along. The very first `-race` run fired a real race
in `internal/playlist`. See PB-1.

**Note on `-p 1`:** the default parallel `go test ./internal/...` interleaves stderr across packages and
**corrupts/loses race reports** — the parallel run showed 39 race blocks, the serialized run showed **296**.
Always use `-p 1` for race sweeps here, or the numbers lie.

---

## 1. Findings

### PB-1 — `PlaybackManager.Cancel()` reads `MediaPlayerRepository` with no lock; writer holds `pm.mu` — **open** (HIGH) — **CONFIRMED (race detector)**

- **Location:** `internal/library/playbackmanager/playback_manager.go:630` (unlocked read) vs `:286` (locked write)
- **Reproduced by:** `go test -race -run TestPlaylistManagerListenToEventsReopensCurrentEpisode ./internal/playlist/`
  → `WARNING: DATA RACE` / `--- FAIL: ... race detected during execution of test`
- **Not introduced by the audit diff.** `playback_manager.go` is *not* among the 39 changed files. This is a
  **pre-existing** race that the new playlist test merely exposed.

**The code (verbatim):**

```go
// writer — :281-289, inside a bare `go func()`
func (pm *PlaybackManager) SetMediaPlayerRepository(mediaPlayerRepository *mediaplayer.Repository) {
	go func() {
		if pm.cancel != nil { pm.cancel() }          // <-- also unsynchronized (see PB-2)
		var ctx context.Context
		ctx, pm.cancel = context.WithCancel(context.Background())

		pm.mu.Lock()
		pm.MediaPlayerRepository = mediaPlayerRepository   // :286  WRITE — under pm.mu
		pm.mediaPlayerRepoSubscriber = pm.MediaPlayerRepository.Subscribe(...)
		pm.mu.Unlock()
		pm.listenToMediaPlayerEvents(ctx)
	}()
}

// reader — :628-632
func (pm *PlaybackManager) Cancel() error {
	pm.Logger.Debug().Msg("playback manager: Cancel called, stopping media player")
	pm.MediaPlayerRepository.Stop()                        // :630  READ — NO LOCK
	return nil
}
```

- **What's wrong:** the write takes `pm.mu`; the read takes nothing. **A one-sided lock provides no
  mutual exclusion** — `pm.mu` here is decorative with respect to `Cancel()`.
- **Why it matters:** two concrete failure modes, not just theory.
  1. **nil dereference → process death.** `SetMediaPlayerRepository` publishes the field from an
     *unsynchronized goroutine*. Nothing orders it against a caller. If `Cancel()` runs before that
     goroutine's `pm.mu.Lock()` section lands, `pm.MediaPlayerRepository` is still `nil` →
     `nil.Stop()` → panic. `Cancel()` has no `defer recover()`, and it is invoked from a background
     goroutine (`playlist.listenToEvents` → `ReopenEpisode` → `playEpisode`, `manager.go:609`), so an
     unrecovered panic there **kills the whole server process**, not just the request.
  2. **Stop() on the stale repository.** On a settings change / player re-mount, `Cancel()` can read the
     *previous* `*mediaplayer.Repository` and stop a player nobody is watching, while the new one keeps
     running — the "cancel did nothing" class of bug.
- **Severity: HIGH** — playback path, reachable from ordinary playlist use, worst case is process death.
- **Suggested fix (narrow):** give the read the same lock the writer already takes:
  ```go
  func (pm *PlaybackManager) Cancel() error {
      pm.mu.RLock()
      repo := pm.MediaPlayerRepository
      pm.mu.RUnlock()
      if repo == nil { return nil }   // or a typed error
      repo.Stop()
      return nil
  }
  ```
  (Requires `pm.mu` to be an `RWMutex`; if it is a plain `Mutex`, use `Lock`/`Unlock` — the critical
  section is a single pointer load.) Snapshot-then-call, so `Stop()` is never invoked while holding the
  lock (avoids inverting lock order against `mediaplayer.Repository`'s own internal locks).
- **Regression risk: LOW-MEDIUM.** `Cancel()` is small and the change is local, but `pm.mu` is shared with
  `SetMediaPlayerRepository`'s critical section which calls `Subscribe()` *while holding the lock*. Calling
  `Stop()` under the same lock could deadlock against the repository's internals — hence the
  snapshot-then-release shape above. **Do not** simply wrap the existing body in `pm.mu.Lock()`.
  The `nil` guard is a behaviour change: `Cancel()` currently panics in that window; after the fix it
  returns cleanly. That is the intent, but any caller relying on the panic (none found) would notice.

### PB-2 — `pm.cancel` read/written outside `pm.mu` in the same function — **open** (MEDIUM) — **PLAUSIBLE**

- **Location:** `internal/library/playbackmanager/playback_manager.go:275-281`
- **What:** `if pm.cancel != nil { pm.cancel() }` and `ctx, pm.cancel = context.WithCancel(...)` both sit
  **above** the `pm.mu.Lock()` line, inside the spawned goroutine. Two concurrent
  `SetMediaPlayerRepository` calls (settings change while mounting) race on `pm.cancel`, and can leak a
  listener goroutine — precisely the thing the comment says the cancel exists to prevent
  ("this is done to prevent multiple listeners").
- **Why it matters:** goroutine leak + the previous listener never stops → duplicate media-player event
  streams, which is a known symptom class in this repo.
- **Verdict PLAUSIBLE:** the racing pair was not independently observed by the detector in this run; reachability
  depends on two concurrent mounts. Listed for completeness, not asserted.
- **Suggested fix:** move the `pm.mu.Lock()` to the top of the goroutine body so `pm.cancel` is covered too.
- **Regression risk: MEDIUM** — widening the critical section to include `pm.cancel()` and
  `listenToMediaPlayerEvents` setup could deadlock if the cancelled listener needs `pm.mu` to exit. Verify
  `listenToMediaPlayerEvents` does not take `pm.mu` before widening.

---

## 2. Race sweep — counts

| Run | Packages | Race blocks |
|---|---|---|
| `-race` default parallelism | `./internal/...` | 39 (reports interleaved/lost — **unreliable**) |
| `-race -p 1` serialized | `./internal/...` | **296 blocks → 66 unique racing pairs** |

66 unique pairs: **48 product-side / 10 test-only / 8 unclear**.

### HARNESS-2 — `[build failed]` for `internal/core` + `internal/cron` is **environmental, not a defect** — **closed**

Root-caused. The `[build failed]` lines appear **only** in the default-parallelism log, never in the `-p 1` log.
No Go source diagnostic exists for either package. The real errors in that run are Windows toolchain
allocation failures:

```
compile.exe: fork/exec ...: The paging file is too small for this operation to complete.
ld.exe: out of memory allocating 1480688 bytes
==16848==ERROR: ThreadSanitizer failed to allocate 0x000000800000 bytes (error code: 1455)
```

Windows error **1455 = `ERROR_COMMITMENT_LIMIT`**. `-race` forces cgo linking for *every* package, so
default parallelism spawns a dozen-plus concurrent `mingw gcc`/`ld.exe` + TSan-instrumented links; whichever
packages lose the allocation race that instant get reported as `[build failed]`. **14 packages** hit it in
that run. Serialized, both build and test clean on the dirty tree *and* the clean baseline worktree
(`race-dirty-p1.log:123-124` → `ok seanime/internal/core`). `internal/cron` has **zero `*_test.go` files**, so
a "test build failure" there is impossible by construction.

**No coverage was lost, no defect exists.** Verdict: environment artifact. Another reason `-p 1` is mandatory.

### HARNESS-3 — host resource exhaustion invalidated a run (process note)

Mid-audit, `C:` hit **100% full (96 MB free of 477 GB)** — the Go build cache had grown to **41 GB**, inflated
by cgo/race objects (race builds are enormous). This silently broke a running workflow (two agents logged
`no space left on device`) and produced a **worthless empty baseline**. `go clean -cache` recovered 48 GB.

Lesson for the next dynamic audit: `-race` sweeps on this host are resource-hungry in two dimensions —
**disk** (build cache) and **commit limit** (page file). Check both *before* trusting a sweep's output. An
ENOSPC-corrupted run does not announce itself; it just produces confidently empty results.

### Environmental failures — not defects

27 packages / ~140 tests fail in the sweep, overwhelmingly for environmental reasons (no network, no
AniList/TMDB/debrid tokens, the known `missing AniList fixture for AnimeCollectionWithRelations`) plus a
Windows temp-file-locking artifact (`TempDir RemoveAll cleanup: ... used by another process`). These are
**not** counted as findings.

---

## 3. Confirmed product races (adversarially verified)

Each below was checked by a frontier verifier **prompted to refute it**, using the code, not the detector's
word. All are **PRE-EXISTING** — provenance independently established (the audit diff does not touch
`internal/plugin/`, and `git diff --stat HEAD -- internal/plugin/` is empty). **None of these is a regression
of the uncommitted diff.**

Root cause for the whole cluster: **goja VMs are not thread-safe.** `internal/plugin/ui/scheduler.go:20-21`
states the invariant outright — *"Any goroutine that needs to execute a VM operation must schedule it because
the UI VM isn't thread safe"*. The races are all violations of that contract.

### RACE-1 — `(*UI).Register` drives the VM directly, bypassing the UI scheduler — **open** (HIGH) — **CONFIRMED**

- **Location:** `internal/plugin/ui/ui.go:190` vs `internal/plugin/ui/dx.go:481` (`debounce`) and `dx.go:507` (`poll`)
- **Both sides are product code** — this is the strongest finding in the cluster: no test-harness excuse.
- **What:** `Register()` calls `u.vm.RunString("(" + callback + ").call(...)")` on the caller's goroutine,
  while `dxJobs.debounce`'s timer goroutine and `dxJobs.poll`'s ticker goroutine correctly route their VM
  work through `scheduler.ScheduleAsync`. `Register` is the side that violates the documented invariant.
- **Why it matters:** concurrent access to a non-thread-safe `*goja.Runtime`. Not a benign field race —
  corrupting a JS VM's internal state can panic or misbehave arbitrarily inside a plugin.
- **Occurrences:** 22 (vs debounce) + 20 (vs poll).
- **Fix:** route `Register`'s `RunString` through the same `gojautil.Scheduler` every other caller uses.
- **Regression risk:** MEDIUM — `Register` currently runs synchronously; scheduling it async changes ordering
  for plugin registration. Verify no caller depends on the callback having run by the time `Register` returns.

### RACE-2 — goja `fetch` resolves promises from a pump goroutine while the caller polls the VM — **open** (HIGH) — **CONFIRMED**

- **Locations:** `internal/goja/goja_bindings/fetch.go:565` (resolve), `:510` (`callable(goja.Undefined())`),
  `:575` (`toGojaObject`), `internal/goja/goja_bindings/bindings.go:18` (`NewError`)
- **What:** `BindFetch` (`fetch.go:225-239`) spawns a pump goroutine that touches the `*goja.Runtime`
  (resolving promises, building result objects, constructing errors) while the caller polls
  `promise.State()` on its own goroutine.
- **Why the test-harness objection fails:** the verifiers established that production uses the **same
  pattern** — `internal/util/goja/async.go:WaitForPromise`, bound to `$await`. The test polls the promise
  exactly as `$await` does, so the racing interleaving is reachable in shipped code, not a test artifact.
- **Occurrences:** 51 + 27 + 27 + 7 + 7 + 7 + 5 + 5 across call sites.
- **Note:** `fetch.go` **is** in the uncommitted diff (+76 lines), but the verifiers confirmed the race
  predates it. Worth re-confirming against the targeted baseline before shipping.
- **Regression risk:** HIGH — this is the shared async plumbing behind every extension's `fetch`. A wrong
  fix here breaks every extension. Prefer the narrowest change that serializes VM touches onto the owner.

### RACE-3 — `AbortSignal` fires listeners onto the VM from a scheduler worker — **open** (HIGH) — **CONFIRMED**

- **Location:** `internal/goja/goja_bindings/abort_context.go:57` (`abort`), `:88` (`toObject`/addEventListener immediate-fire)
- **What:** `s.aborted`/`s.reason` *are* mutex-protected — the race is on the **VM touched by the callback
  invocation itself**, while a synchronous `vm.RunString` of another `controller.abort()` is still executing.
- **Occurrences:** 21 + 21.

### RACE-4 — `plugin.Storage.Watch` invokes JS callback off the owner goroutine — **open** (MEDIUM) — **CONFIRMED**

- **Location:** `internal/plugin/storage.go:485-486`
- Same scheduler-worker-vs-direct-caller pattern. Occurrences: 7.

### RACE-5 — `settingsAction` vs `TestGetIncludes` — **REFUTED, not a finding**

Refuted on two independent grounds: provenance wrong (`git diff --stat HEAD -- internal/plugin/` empty) and
the racing pair is test-harness-induced. Recorded so it is not re-chased.

---

## 4. Headless harness — proven working

`-tags nosystray` is the key. The default Windows build (`server_windows.go`) blocks on `systray.Run` +
`hideConsole()`; `server_windows_nosystray.go` calls `startAppLoop` directly — a plain blocking call, ideal
for scripted harnessing.

```bash
cd H:/Projects/seanime
CGO_ENABLED=0 go build -tags nosystray -o seanime-test.exe .
./seanime-test.exe --datadir <throwaway> --port 43299 --host 127.0.0.1 \
                   --admin-username admin --admin-password <pw>
```

Flags (`internal/core/flags.go:28`): `--datadir --host --port --update --desktop-sidecar --disable-features
--disable-all-features --password --disable-password --admin-username --admin-password`.
Env equivalents: `SEANIME_DATA_DIR`, `SEANIME_SERVER_HOST`, `SEANIME_SERVER_PORT`.

- **No onboarding gate**; no AniList needed (falls back to `SimulatedPlatform`); no debrid needed
  (missing provider → clean `500 {"error":"debrid: Provider not set"}`, not a crash).
- **First-run admin**: with no `--admin-password`, `bootstrapAdminUser` (`internal/core/modules.go:523`)
  generates a random password and **only logs it** — you must read the log to learn it.
- **Gotcha:** `/api/v1/settings` + `/api/v1/torrent-client/list` transiently 500 for ~1s after listen while
  modules initialise. Not a defect — don't probe instantly after "Seanime started at".
- **Cannot be tested headless:** MpvCore/playback (Electron-only), systray boot path, real AniList sync,
  real debrid resolve, `--desktop-sidecar`, `--update`.

### AUTH-0 — password-less local install = everyone is admin — **CONFIRMED live** (by design, but see below)

Proven two ways, code + live:
1. `OptionalAuthMiddleware` (`internal/handlers/server_auth_middleware.go:11`):
   `if h.App.Config.Server.Password == "" { return next(c) }` — **every** `/api/v1/*` route skips the gate.
2. `IdentityMiddleware` (`internal/handlers/identity.go:35`): no session + no server password → resolves the
   request to `GetAdminUser()` and stamps admin role into context.

Live: `curl /api/v1/user/me` with zero headers → `{"id":1,"username":"admin","role":"admin"}`;
`PATCH /api/v1/settings` (admin-only) → 200 with no credentials.

**Correction to `audit-test-2026-07-16.md`.** That guide claims the auth tests are only meaningful against
"the networked/password setup" and therefore effectively untestable locally. **That is wrong** — the
`--password` flag configures exactly that setup locally. AUTH-1..4 are testable on this machine, pre- and
post-fix, without touching prod. See §5.
