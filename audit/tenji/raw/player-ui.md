# Tenji audit — player UI sweep

Scope: `H:/Projects/seanime-tenji/src/components/features/player/` only (assigned sub-scope of a larger multi-agent Tenji audit). Hunt targets: correctness bugs, crashes, races, perf (re-renders, JS-thread stalls, leaks), UX defects, UI defects, security/privacy. Specific named concerns: controls layout + gesture handlers (incl. use-side-adjust), overlays, subtitle display, progress-bar seeking, orientation/fullscreen, PiP, whole-player re-renders during playback.

Excluded per task: feature-parity gaps vs web/Denshi, known deferred parity items, by-design issues (pause-on-lock passcode, nakama same-account collision, EAS Windows build), anything already fixed in `H:/Projects/seanime/tenji-audit.md` §0 ledger, `src/api/generated/` style.

## Files read (full, in scope)

- `constants.ts`
- `types.ts`
- `helpers.ts`
- `hooks/use-controls-visibility.ts`
- `hooks/use-double-tap-seek.ts`
- `hooks/use-landscape-orientation-lock.ts`
- `hooks/use-player-gestures.ts` (~597 lines)
- `hooks/use-side-adjust.ts`
- `hooks/use-skip-data.ts`
- `hooks/use-swipe-seek.ts`
- `hooks/use-auto-next-episode.ts`
- `player-controls.tsx` (420 lines)
- `player-overlays.tsx`
- `player-panel.tsx` (1271 lines)
- `player-auto-next.tsx`
- `external-player-picker-sheet.tsx`

## Files read outside formal scope (cross-reference only, not audited themselves)

- `app/(app)/(media)/player.tsx` — spot-read around: chapters memoization + `useSkipData` call site (~200-260), `displayTime`/`progressRatio` derivation (~907-911), main overlay JSX composition (~1020-1090, 1160-1199) to verify how in-scope components are mounted/gated and what props they receive.
- `src/lib/player/player-preferences.ts` — confirmed `createMMKV(...)` is synchronous, ruling out an async-race hypothesis in `player-panel.tsx`'s `ExternalSubtitleSearchContent`.

## Findings

### F1 — "Forward / Back Seek" settings screen and its buttons are fully wired but unreachable dead code

**Files:** `player-controls.tsx:107-108,127,340-364`, `player-panel.tsx:59` (PANEL_META), `player-panel.tsx:206-215` (SeekAmountContent render for `"seek-buttons"`), `types.ts:9` (`"seek-buttons"` in `PlayerPanel` union), `helpers.ts` (`getBackPanel` still has `case "seek-buttons": return "main"`).

Evidence — `player-controls.tsx` (props are destructured and only used inside a commented-out block):
```tsx
// props (line ~107-108)
onSeekRelative: (delta: number) => void
buttonSeekSec: number
...
// lines ~340-364
{/* <Pressable onPress={() => onSeekRelative(-buttonSeekSec)}>
      <RotateCcw ... />
    </Pressable>
    <Pressable onPress={() => onSeekRelative(buttonSeekSec)}>
      <RotateCw ... />
    </Pressable> */}
```

`onSeekRelative` and `buttonSeekSec` are passed in from `player.tsx` (`onSeekRelative={player.seekRelative}`, `buttonSeekSec={prefs.buttonSeekSec}`) but are dead: nothing in the rendered JSX of `ControlsOverlay` calls `onSeekRelative` or reads `buttonSeekSec` outside the comment.

Separately, `player-panel.tsx`'s `MainSettingsContent` `rows` array (the main settings screen's navigable row list, ~lines 350-420) has rows for Playback Speed, Double-Tap Seek, Audio & Subtitles, Auto Next Episode, Auto Skip OP/ED, Tap to Play & Pause, Gesture Controls, Picture-in-Picture, Screen Lock — but **no row with `panel: "seek-buttons"`**. Grepped `player.tsx` for `"seek-buttons"`/`setPanel(` — no other navigation entry point exists anywhere in the app.

**Failure scenario:** a user can never open the "Forward / Back Seek" settings screen (`PANEL_META["seek-buttons"]`, title "Forward / Back Seek") because no button/row anywhere sets `panel` to `"seek-buttons"`. The physical rewind/fast-forward buttons that would consume `buttonSeekSec` are commented out in the controls bar, so the entire button-seek feature is invisible and unusable, while its state/prefs plumbing (`prefs.buttonSeekSec`, `BUTTON_SEEK_OPTIONS` in `constants.ts`) stays alive and maintained for nothing.

**Suggestion:** either add a `MainSettingsContent` row that navigates to `"seek-buttons"` and uncomment the rewind/forward buttons in `ControlsOverlay`, or delete the panel case, the `PANEL_META` entry, the `PlayerPanel` union member, `getBackPanel`'s case, and the commented block — whichever the feature call turns out to be.

Severity: low. Category: smell (dead code, not a crash/data-loss risk, but wasted surface + confusing for future maintainers).

---

### F2 — `ControlsOverlay` receives the raw ticking `state` object and is not memoized, so it fully re-renders on every playback timer tick while mounted

**Files:** `player-controls.tsx:117` (`export function ControlsOverlay(props: ControlsOverlayProps)`, no `React.memo`), `app/(app)/(media)/player.tsx:1028-1060` (mount site), `app/(app)/(media)/player.tsx:1032` (`state={state}`), `app/(app)/(media)/player.tsx:907` (`displayTime = swipeSeek.swipeSeeking?.currentTime ?? seekingDisplay ?? state.currentTime`).

`ControlsOverlay` is rendered unconditionally whenever `!isPiPActive` — it is not conditionally unmounted when controls are hidden (control visibility is purely an opacity/pointerEvents animation, confirmed via `use-controls-visibility.ts`), so it stays mounted and subscribed to `state` for the entire duration of playback, hidden or not:

```tsx
// player.tsx ~1028-1060
{!isPiPActive && (
    <ControlsOverlay
        ...
        state={state}
        displayTime={displayTime}
        ...
    />
)}
```

`state` is the live `PlayerStateType` object whose `currentTime` field updates on (effectively) every player tick — this is the same object `displayTime` falls back to (`state.currentTime`) at player.tsx:907. `ControlsOverlay` is a plain function component (`player-controls.tsx:117`) with no `React.memo`, so every tick forces React to re-run the whole component: re-derive `SegmentFill` progress bar segments, rebuild the chapter-segmented seek bar's child array, recompute all the Pill/IconButton JSX, etc. — even while `controlsVisible` is false and the overlay is fully transparent/non-interactive.

**Failure scenario:** on a device under load (long chapter lists, many segments in the chapter-segmented seek bar, older iPhone), the once-per-tick re-render of the entire controls tree (not gated by visibility) competes with the JS thread work the gesture/pan/side-adjust hooks are already doing, and is pure waste while controls are hidden — this is exactly the pattern the audit brief calls out ("state changes that re-render the whole player during playback").

**Suggestion:** narrow what `ControlsOverlay` actually needs from `state` (most of the visible chrome — title, speed pill, chapter segments — doesn't need `currentTime` at 1-tick granularity; only the seek-bar thumb position and the time text do), wrap `ControlsOverlay` in `React.memo`, and/or hoist just the ticking bits (progress fill / time text) into a small child component so the rest of the overlay doesn't re-render every tick.

Severity: medium. Category: perf.

---

### F3 — Commented-out "Custom URL scheme" external-player option leaves orphaned live state/handlers

**File:** `external-player-picker-sheet.tsx:20-21,57-66,107-129`.

```tsx
const [selected, setSelected] = React.useState<string | null>(null)
const [customTemplate, setCustomTemplate] = React.useState("")
...
const handleSelectCustom = () => { setSelected(CUSTOM_ID) }
const handleCustomTemplateChange = (text: string) => {
    setCustomTemplate(text)
    setPlayerPreferences({ externalPlayerTemplate: text || null })
}
const isCustomActive = selected === CUSTOM_ID
...
{/* <Surface variant="muted" className="overflow-hidden">
 <OptionRow label="Custom URL scheme" ... onPress={handleSelectCustom} />
 {isCustomActive && (<View>...<TextInput ... onChangeText={handleCustomTemplateChange} />...</View>)}
 </Surface> */}
```

Same shape as F1: `customTemplate`, `isCustomActive`, `handleSelectCustom`, `handleCustomTemplateChange` are all defined and exercised only by JSX that is commented out, so they never run. Distinct from F1's dead panel — this disables a whole configuration path (custom external-player URL scheme) that the preset-only UI can't otherwise reach: `getPlatformExternalPlayers()` returns a fixed preset list, so any external player not in that list can't currently be configured through the UI even though `PlayerPreferences.externalPlayerTemplate` (the underlying pref) supports an arbitrary template string.

**Failure scenario:** a user whose preferred external player isn't in the hardcoded preset list has no way to set a custom `externalPlayerTemplate` via the UI — the field can only be populated by a preset tap or (previously) the now-disabled custom-scheme input.

Severity: low. Category: smell (dead code / disabled feature path, not a crash).

---

### F4 — AniSkip fetch has no timeout/abort signal

**File:** `hooks/use-skip-data.ts:96-114`.

```ts
let active = true
fetch(`https://api.aniskip.com/v2/skip-times/${malId}/${epNum}?types[]=ed&types[]=mixed-ed&types[]=mixed-op&types[]=op&types[]=recap&episodeLength=`)
    .then(res => res.json())
    .then(data => { if (!active) return; ... })
    .catch(() => { if (active) setSkipData(chapterSkip) })

return () => { active = false }
```

No `AbortController`/`AbortSignal.timeout(...)` is attached to this `fetch`. The `active` flag correctly prevents a late response from calling `setSkipData` after unmount/dep-change (no state-update-after-unmount bug), but the underlying network request itself is never aborted — on a slow/dead connection it can hang for the platform's default timeout (60s+ on iOS) doing nothing useful, holding a socket/radio open, before the promise ever resolves or the effect cleanup fires. The app's own `requests.ts` layer already learned this lesson (ledger item T4 in `tenji-audit.md`: `signal: AbortSignal.timeout(15_000)`), but this hook calls the third-party AniSkip API directly with a bare `fetch`, bypassing that fix.

**Failure scenario:** user on a flaky/degrading cellular connection opens an episode with no chapter-based OP/ED metadata (`chapterSkip.op`/`chapterSkip.ed` both null) → the AniSkip fetch is issued and hangs; not user-visible as a crash (silently falls back to no skip data since `active` still gates the `.catch`), but it's a dangling long-lived request per-episode-open with no cap, which is exactly the anti-pattern already fixed elsewhere in the codebase.

**Suggestion:** `fetch(url, { signal: AbortSignal.timeout(10_000) })`, matching the existing `requests.ts` pattern.

Severity: low. Category: perf.

## Near-misses investigated and rejected (not filed)

- **`use-player-gestures.ts` stale-closure-in-`useMemo(() => {...}, [])` hypothesis** — the gesture composition tree is built once via an empty-deps `useMemo`, which looks suspicious at a glance. Traced every captured handler: all bottom out in `useCallback`s whose only deps are stable refs (`gRef`, `latestRef`, both updated unconditionally every render via `syncGestureRef`/direct assignment), so the handlers always read fresh values despite the gesture object itself never being recreated. Not a bug.
- **Single-tap vs native double-tap race in edge zones (`use-player-gestures.ts`)** — double-tap-seek in edge zones and center-tap play/pause both use `Gesture.Tap` primitives; investigated whether a single tap in an edge zone could double-fire (once as "tap", once as a suppressed/delayed double-tap). The `pendingSideTapRef`/`suppressedEdgeTapRef` handshake correctly suppresses the single-tap handler's effect when a same-zone double-tap is pending. Not a bug.
- **`use-skip-data.ts` effect depending on `chapters` (array identity)** — worried the effect (re-fetches AniSkip / recomputes chapter skip data) could re-run every render if `chapters` isn't referentially stable. Confirmed the `player.tsx` call site memoizes `chapters` before passing it down. Not a bug.
- **`use-landscape-orientation-lock.ts` accelerometer + orientation-listener + AppState interplay** — reviewed for re-lock races (e.g. unlock-on-unmount racing a foreground re-lock) but found no concrete reachable bad state; the unmount cleanup unlocks native orientation then defers `ScreenOrientation.lockAsync` via `requestAnimationFrame` + `InteractionManager.runAfterInteractions`, which serializes after any in-flight listener callback. Android-specific paths in this file were not chased further since iOS is the only shipped target.
- **`use-side-adjust.ts` Android brightness-restore ordering** — `initialUsesSystemBrightnessRef.current` can theoretically still be `null` at unmount if the `Brightness.isUsingSystemBrightnessAsync()` await hasn't resolved yet, which would make the unmount cleanup's `!== false` check take the "restore system brightness" branch by default. This is real but Android-only (iOS branch of the same cleanup uses `hasInitialBrightnessRef`/`initialBrightnessRef`, which are populated synchronously by the sync effect on mount and are correct). Out of scope: Android is not a shipped target for Tenji.
- **`player-controls.tsx` commented-out inline "Lock" icon button (lines ~241-246)** — looked like the same dead-code shape as F1/F3, but the lock feature is NOT dead: it's reachable via the main settings panel's "Screen Lock" row (`onLockScreen={controls.lockScreen}` wired through `player-panel.tsx`/`player.tsx:1195`). This is a relocated control, not an orphaned one. Not filed.
- **`player-overlays.tsx` `_WORKLET` guard inside a `useAnimatedReaction` callback in `SideAdjustHUD`** — odd defensive check that seems redundant since `useAnimatedReaction`'s reaction function always executes on the UI thread already, but it's harmless (never false in practice) — noted as a curiosity, not a finding.
- **`constants.ts` `PAN_GESTURE_MIN_DISTANCE(16) > SWIPE_ACTIVATION_THRESHOLD(12)` / `SIDE_ADJUST_ACTIVATION_THRESHOLD(12)`** — the activation thresholds are smaller than the pan gesture's own `minDistance`, making them effectively unreachable/redundant (the pan gesture never activates below 16px, so a 12px activation check downstream never sees values below 16px). Cosmetic redundancy, not a behavioral bug, not filed.
- **`use-auto-next-episode.ts` `cancelAutoNext`/`triggerAutoNext` not wrapped in `useCallback`** — new function identity every render; could cause downstream effect/memo churn if consumers depend on them, but checked the consumer (`player.tsx`) and it doesn't put them in any dependency array in a way that causes a visible loop or extra work. Minor smell, not filed as a separate finding (same category as F2 but far lower impact/evidence).

## Coverage notes

Every file under `src/components/features/player/` was read in full. The chapter-segmented seek bar's actual drag/seek gesture implementation (`seekBarGesture`) is constructed in `app/(app)/(media)/player.tsx`, outside this formal scope, and only consumed via props inside `ControlsOverlay` — the seek-bar drag mechanics themselves (as opposed to how the result is rendered) were not audited at the source, since that file belongs to a different sweep-agent's scope. No PiP-lifecycle-specific bug was found within this directory; PiP entry/exit orchestration itself also lives in `player.tsx`, not in this scope — this directory only reads `isPiPActive` as a boolean gate.
