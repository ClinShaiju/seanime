# Sweep: modules/expo-mpv-player/ (iOS Swift + TS bridge)

Scope: view lifecycle, observer/KVO registration+removal (incl. protectedDataWillBecomeUnavailable
pause-on-lock), event dispatch to JS, error paths, memory leaks, thread safety, drift between TS API
surface and native implementation. Android ignored per instructions (not a shipped target).

## Files read (full)

- `modules/expo-mpv-player/ios/MpvPlayerView.swift` (MpvSurfaceExpoView — the ExpoView, PiP/NowPlaying glue)
- `modules/expo-mpv-player/ios/MPVLayerRenderer.swift` (core libmpv wrapper, event pump)
- `modules/expo-mpv-player/ios/PiPController.swift`
- `modules/expo-mpv-player/ios/MPVNowPlayingManager.swift` (singleton)
- `modules/expo-mpv-player/ios/ExpoMpvPlayerModule.swift` (module definition, prop/function/event wiring)
- `modules/expo-mpv-player/src/index.ts`, `MpvPlayer.types.ts`, `MpvPlayerModule.ts`, `MpvPlayerView.tsx`
- `modules/expo-mpv-player/expo-module.config.json`, `ios/ExpoMpvPlayer.podspec`, `package.json` (config sanity only)
- Cross-checked consumers: `src/lib/player/use-mpv-player.ts`, `app/(app)/(media)/player.tsx`,
  `src/components/features/player/hooks/use-landscape-orientation-lock.ts`,
  `src/components/features/player/hooks/use-side-adjust.ts`
- Checked `H:/Projects/seanime/tenji-audit.md` ledger first (T1-T13) — none overlap this module.

## Findings (kept — see structured output for full text)

1. **CRITICAL — use-after-free**: `MPVLayerRenderer`'s mpv wakeup callback is registered with
   `Unmanaged.passUnretained(self)` (`start()`, ~line 183-189). `deinit { stop() }` only
   *schedules* `mpv_set_wakeup_callback(handle, nil, nil)` on `queue` asynchronously
   (`stop()`, ~line 192-221) instead of clearing it synchronously before the object can be
   deallocated. Every player teardown (user backs out of the player screen) goes through this
   exact path; if mpv's internal thread fires the wakeup callback in the window between
   `deinit` returning (object freed by ARC) and the queued unregister running, the C callback
   resolves `ctx` via `takeUnretainedValue()` into a dangling pointer and calls
   `.processEvents()` on freed memory. Given mpv signals property changes very frequently during
   active playback (time-pos, demuxer-cache-duration, etc.), this window is hit with real
   probability, not just a theoretical TOCTOU.

2. **HIGH — stuck loading / silent playback failures**: `MPVLayerRenderer.handleEvent(_:)`
   (lines ~376-451) has no `case MPV_EVENT_END_FILE`. `isLoading` is only cleared on
   `MPV_EVENT_FILE_LOADED` / `MPV_EVENT_PLAYBACK_RESTART` / the `paused-for-cache` property.
   None of those fire when `loadfile` fails outright (dead/expired debrid link, DNS/network
   failure, unsupported codec at open). `onError` is only ever emitted from
   `MpvSurfaceExpoView.startRenderer()`'s renderer-creation catch block — never for playback/load
   failures. Net effect: JS sees `isLoading:true` forever, no `onError`, `PlayerState.status`
   stays "loading"/"buffering" indefinitely. Confirmed no client-side watchdog/timeout exists
   (`use-mpv-player.ts`, `player.tsx` — grepped, none). User is stuck on the spinner with no
   recoverable UI state.

3. **MEDIUM/HIGH — unsynchronized cross-thread access to `mpv`/`isRunning`**: unlike the
   `cachedPosition`/`isPausedState`/etc. group (properly funneled through `stateQueue` — see
   "Thread-safe accessors" section, lines ~69-102), the `mpv` handle itself, `isRunning`, and
   `isStopping` are plain `var`s with no synchronization. `load()` is the only mutator that
   dispatches onto `self.queue`; every other public command (`play`, `pause`, `seek`, `setSpeed`,
   all subtitle/audio getters+setters, `getTechnicalInfo`, etc., lines ~606-813) reads `mpv`
   directly on whatever thread the Expo `AsyncFunction` runs on. `stop()` (called from
   `MpvSurfaceExpoView.deinit`, itself on the main thread during RN unmount) nils `mpv`
   synchronously on the calling thread while a command call in flight on a different thread may
   already have captured the handle and be about to call an mpv API against it just as
   `mpv_terminate_destroy` runs on `queue`. `use-mpv-player.ts`'s `stop()` explicitly fires
   `ref.stopPictureInPicture()` and `ref.pause()` (fire-and-forget, `.catch()`-only) immediately
   before/around navigation-triggered unmount, which is exactly this pattern.

4. **MEDIUM — half-wired native landscape lock**: `ExpoMpvPlayerModule.lockLandscape()` /
   `unlockOrientation()` (lines 22-40) only `NotificationCenter.default.post(name:
   ExpoMpvPlayerOrientationLockChanged, ...)`. Grepped the entire repo (native + JS, incl. any
   config plugin / AppDelegate source) — nothing anywhere observes that notification name. Yet
   `use-landscape-orientation-lock.ts` explicitly calls `MpvPlayerModule.lockLandscape()` /
   `unlockOrientation()` on iOS specifically (gated `Platform.OS === "ios"`, lines 29-47) as a
   deliberate addition alongside `ScreenOrientation.lockAsync(...)`, implying it's meant to do
   something the `expo-screen-orientation` call alone doesn't cover. It's dead code that looks
   wired (JS calls it, native "handles" it) but does nothing observable.

5. **LOW/smell — TS/native event surface drift**: `MpvPlayer.types.ts` declares
   `onPictureInPictureChange` on `MpvPlayerViewProps`, and `use-mpv-player.ts` defines
   `onNativePictureInPictureChange` and wires it as a prop in `player.tsx:1004`. But
   `ExpoMpvPlayerModule.swift`'s `Events(...)` list (line 225) only registers `"onLoad",
   "onPlaybackStateChange", "onProgress", "onError", "onTracksReady"` — there is no
   `onPictureInPictureChange` EventDispatcher anywhere in `MpvSurfaceExpoView`. PiP state changes
   are only ever delivered via `onPlaybackStateChange({isPiPActive: ...})`, which
   `use-mpv-player.ts` already also handles, so this isn't user-visible today, but the dedicated
   handler/prop is entirely dead — never invoked.

## Rejected / near-misses (checked, not reporting)

- `lockObserver` (protectedDataWillBecomeUnavailable pause-on-lock) — registered once in `init()`
  with `[weak self]`, removed in `deinit`, guards `isPausedState` before pausing. Correctly
  implemented; matches the excluded "needs a device passcode" caveat already called out in the
  audit brief. No bug.
- `statusObservation` KVO on `displayLayer.status` — invalidated in `deinit` before the handle is
  nil'd; the recovery path (`performDecoderReset`) dispatches onto `queue` correctly. Fine.
  `stop()` also invalidates it synchronously (not deferred), consistent.
  correctly. Fine.
- `MPVNowPlayingManager.shared` being a singleton — checked for cross-instance bleed (rapid player
  remount without deinit running first). `MpvSurfaceExpoView.deinit` unconditionally calls
  `clearNowPlayingInfo()` (removes remote-command targets, resets `isCommandsSetup`) before the
  next view could plausibly call `setupRemoteCommands()` again from a fresh `play()`. No concrete
  interleaving found that leaves the Now Playing singleton stuck referencing a torn-down view's
  handlers. Not reporting — too speculative without a stronger interleaving to point to.
- `setWindowBrightness` iOS `Function` is a literal no-op (`{ (brightness: Double) in }`) — but
  `use-side-adjust.ts` only calls `MpvPlayerModule.setWindowBrightness` when
  `Platform.OS === "android"`; iOS uses `expo-brightness`'s `Brightness.setBrightnessAsync`
  instead. The iOS stub is unreachable dead code, not a functional bug (iOS is the only shipped
  target, so this is inert either way). Skipped.
- `MPVNowPlayingManager.refresh()`'s `guard duration > 0 else { return }` delays Now
  Playing/lock-screen metadata (title/artwork) until the first `duration` property update arrives
  after file load — a few hundred ms at most, self-resolves once `duration` property fires via
  `refreshProperty`. Cosmetic, not worth reporting as a defect.
- Data race on plain `mpv`/`isRunning`/`isStopping` vs. Swift memory model formally (word-tearing)
  — folded into finding #3 rather than reported separately; the concrete crash-causing instance is
  #1 (wakeup callback), #3 covers the broader "any command call vs. teardown" pattern.
- PiP delegate/retention graph (`PiPController.delegate` weak, `MPVLayerRenderer.delegate` weak,
  `AVPictureInPictureController.delegate` — Apple-managed) — no retain cycles found.
- `withCStringArray` in `MPVLayerRenderer` — manually `strdup`/`free`s each arg with a `defer`;
  correct, no leak.
- Artwork `URLSessionDataTask` in `MPVNowPlayingManager.setMetadata` — cancelled and replaced on
  every metadata change (`artworkTask?.cancel()`), no leaked in-flight tasks accumulating.

## Coverage

Read every non-Android file in `modules/expo-mpv-player/` (ios/*.swift, src/*.ts/.tsx, config
files). Cross-checked the TS API surface (`MpvPlayerViewRef`, `MpvPlayerViewProps`,
`ExpoMpvPlayerModuleType`) against the native `Prop`/`AsyncFunction`/`Function`/`Events`
declarations in `ExpoMpvPlayerModule.swift` method-by-method — no other surface gaps found beyond
#4/#5 above (every other TS-declared method has a matching native `AsyncFunction`/`Prop`, and vice
versa). Did not audit `android/` (excluded — not a shipped target per task instructions). Did not
re-review the `tenji-audit.md` T1-T13 items (all previously fixed, none touch this module anyway).
