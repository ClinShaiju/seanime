# Audit — native mpv window backend (`feat/mpv-native-window`)

Date: 2026-08-19. Scope: the 5 commits on `feat/mpv-native-window` (`209b7cd3..446ca535`) plus the uncommitted
stats/mini-player fixes in the working tree. Reference doc: `mpv-native-window.md`.

**Investigation only — no code changed by this pass.** Two questions drove it: (1) what else regressed, and
(2) is the `video-sync=display-resample` goal actually working and verifiable.

Legend: severity **HIGH** (breaks a working feature / silent failure), **MED** (wrong behavior, recoverable),
**LOW** (latent, cosmetic, or perf). Status: open / fixed / deferred.

---

## Part A — the resample goal: applied, but not verifiable in-app

### A1. Display-sync options are correctly applied — CONFIRMED — status: open (verification only)

`mpv-core-player-inner.tsx:205-211` sets `video-sync=display-resample`, `interpolation=yes`,
`tscale=oversample` on the native backend only, and only when the user's own mpv config didn't already set
them (`if (!("video-sync" in parsed))`). Those land as CLI args *after* the user's `--include=<config>`
(`session.ts:207-216`), so precedence is right: user config wins by virtue of the `in parsed` guard, and
nothing silently overrides it.

`--display-fps-override` is pushed from `screen.getDisplayMatching(uiWindow.getBounds()).displayFrequency`
(`session.ts:200-205`). The harness (`tests/mpv-native-session.mjs:100`) asserts `vsync-ratio > 0`, which is
real evidence display-sync engaged at least once, on the test machine.

So the mechanism is in place. The problem is everything below.

### A2. `display-sync-active` and `vsync-ratio` are observed but thrown away — CONFIRMED — HIGH (for the stated goal) — status: FIXED 2026-08-19

`mpv-core-player-inner.tsx:215-227` adds `display-sync-active`, `vsync-ratio` and `mistimed-frame-count` to
the observe list. The property handler then filters:

```ts
// mpv-core-player-inner.tsx:1179
if (!diagnosticsProperties.has(event.name)) return
```

and `diagnosticsProperties` (`mpv-core-player-inner.tsx:1787-1798`) contains **neither `display-sync-active`
nor `vsync-ratio`**. Both events are received and dropped. `mistimed-frame-count` does reach the
`frameDrops` atom, but its StatLine is commented out (`mpv-core-stats.tsx:157`).

Net: the three properties added specifically to prove display-sync works are all invisible in the app. The
only display-sync-adjacent number on screen is `display-fps`, which just echoes `--display-fps-override` back
— it cannot disprove a wrong override. **The stated S5 goal is unverifiable outside the harness.**

Fixed: `vsync-jitter` and `vo-delayed-frame-count` added to the observe list, the three display-sync
properties added to `diagnosticsProperties`, a "Display Sync" StatLine added
(`active - ratio N - jitter X` / `inactive`), and the Mistimed / Delayed line uncommented. The prism path now
shows `Display Sync: inactive`, which is accurate — display-resample is only set on the native backend.

Verified by `npm run test:mpv-native` (16/16 PASS) on a 144 Hz panel with a 24 fps clip:
`ratio=6`, `jitter=0.000272`, `mistimed=0`, `display fps matches monitor (mpv=144 electron=144)`.
The harness gained a hard `vsync jitter low` assertion (`< 0.02`) so a wrong refresh rate fails the test
instead of silently degrading playback.

### A3. `--display-fps-override` is a startup-only guess and is never recomputed — CONFIRMED — MED — status: FIXED 2026-08-19

`session.ts:200-205` computes the rate once, at spawn. The `move`/`resize`/`enter-full-screen` listeners
(`session.ts:166-169`) only call `applyBounds()`; nothing re-sets `display-fps-override` over IPC. Drag the
window from a 144 Hz laptop panel to a 60 Hz external monitor and mpv keeps resampling to 144 for the rest of
the session — audio resampled to the wrong clock, rising `mistimed-frame-count`, judder. With a warm player
(see B1) the session can be hours old before playback even starts, so "which monitor was I on at app launch"
is the wrong question entirely.

Second, subtler problem: `displayFrequency` is Electron's **integer** rate. A 59.94 Hz panel reports 60, and
`display-fps-override` makes mpv *trust* that number instead of measuring vsync intervals itself. Not
triggered on this machine — 144 Hz is exactly 144, and the measured jitter of 0.000272 confirms the override
matches reality here. Still unproven on a 59.94/23.976 Hz panel, which is where it would bite. mpv on
Windows with `vo=gpu` can normally query the real rate through DXGI; overriding may be actively worse than
not setting it. Worth an A/B once A2 makes `vsync-jitter` visible.

Fix: recompute on `move`/`enter-full-screen` and push
`set_property display-fps-override <rate>` when the matched display changes; or drop the override entirely if
mpv's own detection proves accurate through `--wid`. Regression risk: low, but the override is load-bearing
for the whole feature — change it behind the A2 instrumentation, not before.

---

## Part B — regressions from the native backend

### B1. A warm native player spawns mpv.exe + a second BrowserWindow at app startup — PARTLY WITHDRAWN — status: FIXED 2026-08-19

`mpv-core.tsx:537-538` keeps `MpvCorePlayerInner` mounted for the app's lifetime
(`keepWarm = __isElectronDesktop__ && !!window.electron?.mpvCore && !!serverStatus…mpvPrismEnabled`). That
comment was written for prism, where "warm" means an idle in-process libmpv instance. On the native backend
the same mount runs `new MpvNativePlayer(...)` (`native/mpv-backend.tsx:44`), which spawns a **real mpv.exe
child process** and a **second transparent BrowserWindow** (`session.ts:117-141`), both alive from launch to
quit whether or not anything is ever played.

`session.ts:162` then calls `videoWindow.showInactive()` unconditionally, before any rect has arrived, so the
window sits at the UI window's *full content bounds* (`session.ts:117-118`). It is behind the opaque UI
window at idle, so it is not visible today — but that's a coincidence of z-order, not a guarantee, and the
initial full-size flash is visible for the frame between drawer-open and the first `setVideoRect`.

**Correction to this finding:** `mpvPrismEnabled` is not a prism-vs-native flag — it selects the player
*engine* (`mpvcore` vs `videocore`, see `playback-settings.tsx:81` and `main-layout.tsx:75`). Gating
`keepWarm` on it is correct; the name is just legacy from when MpvCore meant mpv-prism. Withdrawn.

Spawning mpv.exe at app start is likewise the intended warm-player behaviour, and is now harmless: the
window it owns stays hidden until a rect arrives, so nothing is painted for a player that never plays.

Fix: don't create the mpv session until the first `load()` on the native backend (lazy `start()`), or at
minimum keep the video window hidden (`videoHidden = true` initially) until the renderer reports a rect.
Regression risk: the warm player exists to hide cold-start latency (memory `mpv-prism-startup-serialization`);
making it lazy on native trades startup cost back in. Prefer the hidden-until-rect variant first.

### B2. `start()` failure is an unhandled rejection with no user-visible error — CONFIRMED — HIGH — status: FIXED 2026-08-19

`native/mpv-native-player.ts:110`:

```ts
constructor(readonly id: string, options = {}) { this.readyPromise = this.start(options) }
```

Nothing attaches a `.catch()` (prism does exactly this at `MpvPrismPlayer.js`: `this.readyPromise.catch(()=>{})`),
and `start()` rejects on a missing mpv binary (`session.ts:113` → `resolveMpvBinary()` throws), a spawn
failure, or a 15 s pipe-connect timeout (`ipc.ts:34-47`). Consequences, in order of severity:

1. No `error` event is ever emitted, so `useMpvPrismEvent(player, "error", …)`
   (`mpv-core-player-inner.tsx:1285`) never fires and `state.playbackError` stays null — the UI sits on the
   loading screen forever.
2. Every subsequent `await this.readyPromise` in every player method rejects, mostly into fire-and-forget
   call sites.
3. Meanwhile `MpvNativeVideo` has already set `data-mpv-native-video="fullscreen"`
   (`native/mpv-native-video.tsx:82`), which makes the page background transparent and hides
   `.UI-AppLayout__root` (`globals.css:181-188`). **The failure mode is a see-through app showing the
   desktop, with floating player controls and no error message.**

This is the single most likely first-run failure: `scripts/fetch-mpv.mjs` not having run, or the
`extraResources` copy missing from a packaged build.

Fix: `this.readyPromise = this.start(options).catch(err => { this.emit("error", { message: … }); throw err })`,
plus a `startFailed` flag so `MpvNativeVideo` doesn't apply the transparency attributes when there's no mpv to
show. Regression risk: none — pure error-path addition.

### B3. Both EOF paths are weakened; auto-next never fires — CONFIRMED — HIGH — status: FIXED 2026-08-19

`mpv-core-player-inner.tsx` finishes playback from two independent signals:

- the `eof-reached` **property** (`:1171`), and
- the `ended` **event** with `reason === "eof"` (`:1270-1283`), from mpv's `end-file`.

CONFIRMED: `eof-reached` is **not observed on the native backend**. It is not in
`BASE_OBSERVED_PROPERTIES` (`native/mpv-native-player.ts:53-73`) and not in the UI's observe array
(`mpv-core-player-inner.tsx:215-227`). mpv-prism auto-observes it in its own hardcoded list, which is why this
works today on prism. So path 1 is definitively dead on native.

CONFIRMED by direct measurement (probe: same mpv build, one clip played to EOF under each setting):

```
--keep-open=yes    end-file: NONE            eof-reached: undefined -> false -> true
--keep-open=no     end-file: {reason:"eof"}  eof-reached: undefined -> false -> undefined
```

`session.ts:186` passes `--keep-open=yes`, which prism does not use. mpv therefore pauses on the last frame
and **never emits `end-file`** — killing path 2 as well. With path 1 already dead, reaching the end of an
episode did nothing on the native backend: no auto-next, no completion, video frozen on the last frame.
User-reported 2026-08-19, matching this prediction.

Fixed: `eof-reached` added to `BASE_OBSERVED_PROPERTIES` (`native/mpv-native-player.ts:62-66`), restoring the
path `mpv-core-player-inner.tsx:1171` already implements. `--keep-open=yes` kept — it is what holds the last
frame during the transition — with the measurement recorded in a comment so nobody "cleans it up" and
re-breaks this. `finishPlayback()` is idempotent via `endedRef` (`:1160`), so both paths firing is harmless.

Verified by `npm run test:mpv-native` (18/18 PASS), which gained two guards driving the real session:
`eof-reached observed at end of file ([null,false,true])` and `keep-open still suppresses end-file`.

### B4. Missing observed properties leave several prism behaviors silently inert — CONFIRMED — MED — status: FIXED 2026-08-19

mpv-prism auto-observes 28 properties; the native backend observed 9. The delta that matters:

| Property | Consumer | Effect on native |
|---|---|---|
| `frame-drop-count`, `decoder-frame-drop-count` | stats overlay | drops read 0/0 — **fixed in working tree** |
| `video-params`, `audio-params` | stats resolution/pixel format | fell back to demuxer values — **fixed in working tree** |
| `avsync` | stats A/V Sync | read "unknown" — **fixed in working tree** |
| `aid` / `sid` / `vid` | `trackSelection` → watch-party `audio-track-changed` / `subtitle-track-changed` | track switches never broadcast to the room — **fixed in working tree** |
| `eof-reached` | `finishPlayback` | see B3 — **still open** |
| `idle-active` / `core-idle` | `state` idle/active event | **not a defect** — the `state` handler (`mpv-core-player-inner.tsx:1191`) only acts on `file-loaded`; nothing consumes idle/active on either backend, so observing them would only add pipe traffic |
| `cache-buffering-state` | buffering indicator | native observes `paused-for-cache` instead and maps it through the same `cache` event; `mpv-core-player-inner.tsx:1150-1153` reads `value["cache-buffering-state"]` off an object *or* treats a number as buffering — `paused-for-cache` arrives as a **boolean**, which hits neither branch (`typeof value === "number"` is false; `Boolean(value["underrun"])` on a boolean is false). Buffering state from that property is dropped. |
| `sub-text` / `sub-start` / `sub-end` / `sub-visibility` | `subtitle` event | mpv renders its own subs on this path so nothing is missing visually, but any DOM-side subtitle styling/positioning UI is now decorative — see B7 |

The `cache-buffering-state` row is worse than "one signal instead of two" — traced through, it actively
corrupts the buffer readout. `paused-for-cache` arrives as a **boolean** on the same `cache` event, and both
consumers assume an object:

```ts
// mpv-core-player-inner.tsx:1149-1156
const isBuffering = typeof value === "number" ? value > 0
    : Boolean(value && (value["underrun"] || Number(value["cache-buffering-state"]) > 0))   // false for `true`
setBuffered(mc_cacheBufferedSeconds(event.state, …))
// mpv-core.tsx:417 -> !value || typeof value !== "object" -> returns currentTime
```

So every `paused-for-cache` toggle (a) reports *not* buffering, and (b) sets `buffered = currentTime`,
collapsing "Buffer Ahead" to 0.00 s until the next `demuxer-cache-state` tick overwrites it. The two
properties alternate, so the readout flickers precisely when the stream is in trouble. `mpv-core-stats.tsx:52`
then reads `cache["cache-duration"]` off the same boolean → `NaN` → falls back to the zeroed `buffered`.

Fix: in `MpvNativePlayer.handlePropertyChange`, emit `paused-for-cache` as
`{ "cache-buffering-state": value ? 100 : 0 }` (or observe `cache-buffering-state` directly, which is what
prism does, and drop `paused-for-cache`). Regression risk: the `cache` event is shared with the prism path —
change the emitter, not the reducer.

### B5. Seeks are not serialized on the native backend — CONFIRMED — MED — status: FIXED 2026-08-19

Prism serializes seeks: one in flight, a newer request *replaces* the queued one (`MpvPrismPlayer.flushSeek`).
`MpvNativePlayer.seek` (`native/mpv-native-player.ts:283-286`) fires every call straight down the pipe. Dragging
the scrub bar emits a seek per pointer-move, so mpv receives and executes dozens of sequential exact seeks,
each a demuxer flush. Expect scrub lag proportional to drag length, and on a debrid/network source a burst of
range requests — which is exactly the failure that `denshi-directstream-seek-reset` was fixed for once
already.

Also `seek()`'s default mode is `"absolute"` where prism's is `"absolute+exact"`
(`native/mpv-native-player.ts:283`). Latent only — every call site in `mpv-core-player-inner.tsx` passes the
mode explicitly (verified all 16) — but it silently makes any future `seek(t)` a keyframe seek on one backend
and an exact seek on the other.

Fix: port prism's queue (in-flight flag + single queued seek that replaces its predecessor). Regression risk:
low, self-contained; watch the watch-party follower path, which seeks programmatically.

### B6. Play/pause and load are not optimistic — CONFIRMED — LOW — status: FIXED 2026-08-19

Prism emits `paused` immediately on `play()`/`pause()`/`load()` and reconciles later. Native sets
`pausedState` and waits for mpv to echo the property back (`native/mpv-native-player.ts:272-278`). Over a named
pipe that's ~1 frame, but the play/pause button and any watch-party broadcast now lag the input rather than
leading it.

### B7. Dead controls and non-gated keybindings on native — PARTLY WITHDRAWN — status: FIXED 2026-08-19

- PiP: button is hidden (`mpv-core-player-inner.tsx:2269`) but the **keybinding is not**
  (`:1514-1516` → `enterPip()` → throws `"picture-in-picture is not available on the native mpv backend yet"`
  → error toast). Cosmetic, but it's a thrown error on a normal keypress.
- `fit`, `maxQueuedFrames`, `lowLatency`, `textureSize`, `flipY`, `presentationMode`, `frameTransport` are
  destructured and dropped by `MpvNativeVideo` (`native/mpv-native-video.tsx:24-26`). `fit="contain"` is the
  only one with user-visible meaning; mpv's default `keepaspect=yes` reproduces it, so this is fine as-is —
  but if a "fill/zoom" control is ever added it will silently do nothing here.
- Subtitle appearance: **withdrawn after checking.** Seanime pushes subtitle styling to mpv as properties
  (`sub-font-size`, `sub-color`, … at `mpv-core.tsx:274-286`), not to a DOM renderer, so it travels the same
  `setProperty` path on both backends and works unchanged on native.

### B8. Video-rect sampling assumes zoom factor 1 — CONFIRMED — MED — status: FIXED 2026-08-19

`session.ts:249-252` documents the assumption ("matches Electron's DIP bounds as long as the page zoom factor
is 1") and nothing enforces it: no `setZoomFactor(1)`, no `zoom-changed` guard. Denshi exposes zoom via the
standard Chromium shortcuts, so a user who has ever pressed Ctrl+= gets an mpv window that is offset and
mis-sized relative to the hole for the whole session, with no clue why.

Fix: multiply the rect by `webContents.getZoomFactor()` in `applyBounds()`, or pin zoom to 1 while a native
session is live. Regression risk: nil.

### B9. `raisePair()` on every focus/show is a z-order fight waiting to happen — UNVERIFIED — LOW — status: open

`session.ts:67-80` calls `videoWindow.moveTop()` + `uiWindow.moveTop()` on `focus`, `show` and `restore`. On
Windows `moveTop` is `SetWindowPos(HWND_TOP)`; doing it on every focus means a click on Seanime yanks both
windows above whatever else the user had layered. Documented as necessary (`mpv-native-window.md`), and there
may be no better option, but it deserves an explicit test with an always-on-top third-party window
(Discord overlay, OBS, a video call PiP).

### B10. Lifecycle gaps in the main process — CONFIRMED — MED — status: FIXED 2026-08-19

From the main-process pass, in priority order:

- **Quit races the graceful mpv shutdown.** `cleanupAndExit()` fires `void disposeMpvNative()` unawaited and
  hard-exits after 500 ms (`index.ts:1192-1215`), while `session.destroy()` waits up to `QUIT_GRACE_MS = 2000`
  for mpv to exit (`session.ts:287-319`). The 500 ms wins, so `mpv.exe` can outlive Denshi. Orphan mpv
  processes hold the audio device and a GPU swapchain.
- **`destroy` racing an in-flight `create`.** The session is only registered after `create()` resolves
  (`index.ts:52-62`); a destroy in that window finds nothing and no-ops, and the create then registers a live
  session with no owner → orphan mpv + orphan window. React remount (playerId/`warmEpoch` change) is the
  realistic trigger.
- **Concurrent `create` for the same playerId** has no lock; the second `sessions.set` orphans the first.
- **A pipe disconnect while mpv is alive is never noticed** — only the child's `exit`/`error` feed `onExit`
  (`session.ts:145-154`), so an IPC-dead-but-alive session just rejects every command with "mpv ipc is not
  connected" until an explicit destroy.
- **No per-command timeout** (`ipc.ts:80-89`): a request whose reply is lost (e.g. to the 4 MB line-buffer
  overflow drop at `ipc.ts:91-105`) hangs forever. Since almost every player method starts with
  `await this.readyPromise` and then awaits a command, one lost reply can wedge the player.

### B11. `takeScreenshot` guards on the wrong bridge — CONFIRMED — LOW — status: FIXED 2026-08-19

`mpv-core-player-inner.tsx:1608` gates on `window.electron?.mpvCore` but the native branch immediately below
uses `window.electron?.mpvNative`. Harmless while both are always registered together; wrong as documentation.

### B12. Backend selection can silently freeze to prism — CONFIRMED (mechanism) — LOW — status: FIXED 2026-08-19

`native/mpv-backend.tsx:16-34`: `isSupported()` is `ipcRenderer.invoke` (async, `preload.js:127`), the result
lands in `nativeSupported` later, and `isMpvNativeBackend()` freezes `sessionBackend` on its **first** call
with whatever the flag holds at that instant. If the first call (the `mpvOptions` `useMemo`,
`mpv-core-player-inner.tsx:205`) beats the IPC round-trip, the whole session runs on prism despite the setting
being on — with no log line and no UI difference beyond "the setting did nothing, restart again".

In practice the probe wins: `keepWarm` depends on `serverStatus` (a network round-trip), so the earliest mount
is well after the IPC reply. That makes this latent rather than live — but it is one refactor of the mount
gate away from being a confusing intermittent bug.

Fix: make the freeze explicit — resolve the probe before rendering the player (Suspense/`use()`), or have
preload expose a synchronous `isSupported` via `ipcRenderer.sendSync` at startup. Regression risk: `sendSync`
blocks the renderer once at startup; acceptable for a boolean.

---

## Part C — the two issues fixed in the working tree (recorded for context)

- `mpv-core-stats.tsx` / `native/mpv-native-player.ts`: frame-drop counters now observed and routed; presenter
  drops hidden on native (structurally impossible — no presenter exists). See B4.
- `globals.css`: the mini player now repaints the app background as a clipped backdrop (`body::before`) instead
  of relying on the shell clip alone, and rounds the hole's corners with four `radial-gradient` patches
  (`body::after`) to match the drawer's `rounded-lg`. Root cause was that only `.UI-AppLayout__root` was
  clipped while `html` was globally transparent, so every gap between UI elements showed the desktop.

---

## Priority

**Fix soon** — all five done 2026-08-19; see each finding for what shipped.
1. ~~B2 — unhandled `start()` rejection → invisible app, no error.~~ DONE.
2. ~~B3 — `eof-reached` not observed.~~ DONE 2026-08-19.
3. ~~A2 — surface `display-sync-active` / `vsync-ratio` / `vsync-jitter`.~~ DONE 2026-08-19.
4. ~~B10 — quit race and create/destroy race (orphan mpv processes).~~ DONE (pipe-death detection still open).
5. ~~B5 — seek serialization.~~ DONE.

**Then**
6. ~~A3 — recompute `display-fps-override` on monitor change.~~ DONE — mpv's own `estimated-display-fps` is
   fed back 6 s after every file-load and every window move. Measured on the dev machine: Electron says 144,
   mpv measures 143.990444 (0.0066% error). Correcting it changed mistimed frames 0 -> 0, so it is not a
   judder source here; it matters for 59.94 Hz-reported-as-60 (15x the error) and for monitor changes.
7. ~~B1 — keep the video window hidden until the first rect.~~ DONE (the window is no longer shown before the
   renderer reports a rect). Still open: mpv.exe itself is spawned at app start by the warm player.
8. ~~B4 — `paused-for-cache` boolean.~~ DONE — root cause was the shared reducer, which now only recomputes
   `buffered` from the demuxer-cache-state object. Fixes the same latent bug on the prism path.
9. ~~B8 — zoom factor in `applyBounds()`.~~ DONE.

**Deferrable** — all closed 2026-08-19 except B9.
- ~~B6~~ optimistic `paused` emit ported from prism. ~~B7~~ `togglePip()` now returns early on native (one
  guard in the shared function, covering the keybinding, the remote payload and the cast overlay); the
  subtitle half was withdrawn. ~~B11~~ screenshot guard now checks the bridge the branch actually uses.
  ~~B12~~ `isSupportedSync` (`ipcRenderer.sendSync`) removes the probe race entirely.
- **B9 remains open and cannot be closed statically** — it needs a human to run Denshi alongside an
  always-on-top window (Discord overlay, OBS, a call PiP) and watch whether `raisePair()` steals z-order.

---

## What to test manually (Denshi installer build, native enabled)

1. **End of episode** — let one play to the last second. Does auto-next fire? Does the completion/progress
   update? (B3 — fixed and harness-covered, but the renderer wiring is only typecheck-verified; this is the
   confirmation.)
2. **Display sync** — with A2's StatLine in place, or via
   `mpv-native` logs: confirm `display-sync-active=yes`, `vsync-ratio` ≈ refresh ÷ fps (6 at 144 Hz/24 fps),
   `vsync-jitter` low, `mistimed-frame-count` not climbing. Then drag the window to a second monitor with a
   different refresh rate and watch all four (A3).
3. **Missing mpv.exe** — rename `binaries/mpv/mpv.exe` and start playback. Expect an error toast; today expect
   a transparent app (B2).
4. **Scrub** — drag the seek bar across the whole timeline in one motion, on a debrid stream. Watch for lag and
   for `directstream … connection reset` in the server log (B5).
5. **Quit during playback** — quit Denshi mid-play, then check Task Manager for a surviving `mpv.exe` (B10).
6. **Track switching in a watch party** — change audio/subtitle track on the native client and confirm the
   other client receives it (B4, fixed in working tree — this is the verification).
7. **Buffering indicator** — throttle the network mid-play and confirm the spinner still appears (B4,
   `paused-for-cache` boolean).
8. **Mini player** — enter/exit, drag it around, check rounded corners and that no desktop shows through
   anywhere (Part C).
9. **Zoom** — press Ctrl+= once, then play. Expect the video window to be offset (B8).
10. **Second monitor with different DPI** — move the window across a scale boundary mid-play; confirm the video
    stays glued to the hole.
