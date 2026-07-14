# Tenji audit — scope: src/lib/player/ (playback orchestration, continuity, prewarm, track selection, session lifecycle)

Date: 2026-07-10. Repo: H:/Projects/seanime-tenji. Cross-checked against H:/Projects/seanime (server, read-only) and H:/Projects/seanime/tenji-audit.md ledger (T1-T13 all fixed, skimmed to avoid re-reporting).

## Files read (all files in src/lib/player/, full read, no sampling)

- session.ts (819 lines) — playback source atoms, `usePlayerEventListener` (ws → navigate to player), `useStartOnlineStreamPlayback`, `useCleanupPlaybackSession`
- use-mpv-player.ts (584) — native mpv view bridge hook
- playback-coordinator.ts (180) — `usePlaybackCoordinator`: local-file / server-local-file / online-stream launch entry points
- use-continuity-sync.ts (138) — periodic/pause/background/unmount continuity flush + completion detection + manual-tracking start/cancel
- debrid-reconnect.ts (55) — `useDebridReconnectResume`, re-issues last debrid stream start after a ws drop+reconnect mid-playback
- player-preferences.ts (169) — MMKV-persisted prefs + `findPreferredTrack`
- local-file-source.ts (80) — builds `MobilePlaybackSource` for downloaded/local-streamed episodes
- server-local-source.ts (122) — resolves + builds source for the "Seanime Server Mobile" loopback/offline server
- source-resolver.ts (38) — builds source for onlinestream
- subtitle-search.ts (218) — Wyzie subtitle search client (mostly pure data/formatting, no state machine concerns)
- types.ts (159) — `MobilePlaybackSource`, `PlayerState`, `PlayerCommand`, etc.
- external-players.ts (165) — external-player URL template resolution + `Linking.openURL` / `ExpoExternalPlayer.open`
- index.ts (57) — barrel export, cross-checked all exports resolve to real symbols

Also read (out-of-scope, for call-site tracing only, not audited line-by-line): `app/(app)/(media)/player.tsx` (relevant sections: resume-on-ready effect ~180-230, `playEpisodeSelection`/`playNextEpisode` ~680-757, `handleBack`/terminate-signal/`closePlayerToEntry` ~614-654), `app/(app)/(tabs)/(profile)/active-stream.tsx` (full), `app/(app)/_layout.tsx` (mount site of `usePlayerEventListener`), server `internal/debrid/client/stream.go` (StreamStatus enum, to check whether a "stopped" debrid status exists — it doesn't, only downloading/ready/failed/started).

## Findings

### 1. [MEDIUM, bug] Continuity flush has no "flush on source change" trigger — local-file next-episode swap can drop the outgoing episode's resume position entirely

`use-continuity-sync.ts:74-109` implements exactly 4 flush triggers, matching the f281fb5 fix description:
- periodic interval while playing (`CONTINUITY_UPDATE_INTERVAL_MS` = 5000ms), effect keyed on `[source, playerState.paused, hasDuration]`
- flush-on-pause (effect keyed on `playerState.paused`)
- flush-on-AppState-background/inactive
- flush-on-unmount (via `flushContinuityRef`, correctly using a ref to avoid the stale-closure trap the comments describe)

All four are correctly implemented and I could not find a hole in any of them individually — the periodic interval's cleanup (`clearInterval(interval)`, line 81) does NOT call `flushContinuityRef.current()` before tearing down, which is fine *if* every path that changes `source` while staying mounted also fires one of the other 3 triggers (pause/background/unmount) around the same time. That assumption is false for one call site:

`app/(app)/(media)/player.tsx:680-707`, `playEpisodeSelection`, `nextEpisodeAction === "local-file"` branch:
```ts
if (source.nextEpisodeAction === "local-file") {
    if (!episode) return
    ...
    const newSource = getLocalEpisodePlaybackSource({...})
    if (!newSource) { ... return }
    setSource(newSource)         // <-- line 704: swaps currentPlaybackSourceAtom in place
    controls.scheduleHide()
    return
}
```
This is reached both from manual episode-picker selection and from `playNextEpisode()` (auto-advance / "up next" prompt) whenever the *current* episode's `nextEpisodeAction === "local-file"` (i.e. any downloaded-episode playback session). Unlike every other `nextEpisodeAction` (torrentstream-*/debridstream-*/onlinestream-play), which all route through `closePlayerToEntry()` (navigates away, unmounts the player screen, and explicitly calls `player.stop()` first — player.tsx:643-654), the local-file path does **not** navigate or unmount. `useContinuitySync` is called once per `player.tsx` mount (`useContinuitySync(player.source, state)` at player.tsx:161), so its interval/pause/background/unmount effects all persist across this in-place swap. The unmount-flush never fires (no unmount happens) and the pause-flush only fires if the user happened to be paused at the exact moment of the swap (not true for a live "next episode" advance).

Net effect: when advancing between downloaded episodes in-place, the outgoing episode's playback position is flushed to the server only by the periodic 5s interval's last tick before the swap. Two concrete data-loss scenarios:
- **Partial loss (typical case):** up to just-under-5s of watched position for the outgoing episode is never persisted (same magnitude the periodic cadence already tolerates in steady state, so borderline).
- **Total loss (edge case, more interesting):** if the swap happens before the very first periodic tick fires — e.g. the user is on a short downloaded clip/recap episode and immediately hits "next episode", or manually skips through several downloaded episodes from the episode-picker within 5s of each — **zero** continuity updates were ever sent for that episode's watched segment; the server's watch-history/resume record for it is whatever it was before this session started (nothing, if it's a first watch).

I verified there is no other flush call anywhere in this transition: `grep flushContinuity` in player.tsx returns no matches, and the `local-file` branch is the *only* direct `setSource(...)` call in player.tsx (`grep 'setSource\('` → 1 hit, line 704).

Fix direction: add a `source?.id`-keyed effect in `use-continuity-sync.ts` that calls `flushContinuityRef.current()` using the *previous* source/playerState right before the new one takes effect (e.g. capture prev values in a ref and flush them in the cleanup of an effect keyed on `source?.id`), or have `playEpisodeSelection`'s local-file branch call `player.flushContinuity()` (already returned from the hook, just unused by the caller) before `setSource(newSource)`.

### 2. [LOW, smell] `MobilePlaybackSource.resumePositionSec` is read but never written — dead/vestigial field

`types.ts:99` declares `resumePositionSec?: number`, consumed in exactly two places (`use-mpv-player.ts:109` → `MpvVideoSource.startPosition`, and `player.tsx:194-196` as a first-choice resume target before falling back to the `watchHistory` query). Repo-wide search (`resumePositionSec:` / `resumePositionSec\s*=`) finds zero assignment sites — none of `local-file-source.ts`, `server-local-source.ts`, `source-resolver.ts`, or `session.ts`'s source builders ever set it. In practice resume works fine because `player.tsx` has a full fallback path via `useGetContinuityWatchHistory()`, so this isn't a functional bug today, but it means the native player's own `startPosition` config is always `undefined` and the field is misleading for anyone extending a source builder expecting it to control resume.

## Rejected / near-miss (investigated, not reported)

- **`useCleanupPlaybackSession` (session.ts:777-819) never resets `activeStreamSessionAtom`.** Initially looked like a stale-state bug (closing the player leaves a `"playing"` torrent/debrid session atom around). Traced all consumers: it's read by `app/(app)/(tabs)/(profile)/active-stream.tsx` and the profile tab index, which implement a dedicated "Active Stream" screen with a "Stop Stream" button — this exists specifically because server-side torrent/debrid streaming sessions **outlive** the mobile player screen by design (the server keeps streaming/seeding after the UI closes), so the atom must persist past unmount for that screen to have anything to show/stop. `active-stream.tsx` clears it itself (`clearLocalStreamState`) on successful stop. Confirmed by session.ts's own comment ("socket status can outlive the launch screen after app reopen", line 354). **Intentional, not a bug.**
- **`debrid-reconnect.ts` re-issue race against a same-tick "failed" ws message on reconnect.** Theoretically the effect could re-issue `startStream` in the same tick a queued `debrid-stream-state: failed` event (that would null `activeStreamSessionAtom`) is still in flight post-reconnect. Very narrow window, no concrete reproducible failure traced (would require the failure to occur specifically during the ws-down window and be queued for delivery exactly at reconnect) — capped below reporting threshold, not included.
- **`onNativeTracksReady` (use-mpv-player.ts:260-317) has no source-id guard**, unlike `onNativeLoad` which checks `event.nativeEvent.url !== videoSource?.url`. Considered a stale-tracks race on rapid source swap, but the native calls (`getSubtitleTracks`/`getAudioTracks`/`getChapters`) query live native state at resolution time (not a snapshot taken at call time), so by the time the promises resolve they reflect whatever the native view is currently showing — no staleness under the ref-reuse model used here. Not reported.
- **"Flush on pause" effect (`use-continuity-sync.ts:85-89`) has `flushContinuity` (not the ref) in its dependency array**, and `flushContinuity`'s identity changes on every `currentTime` tick while playing. This looks like it would re-run the effect every tick, but the guard is `playerState.paused` — while genuinely playing, `currentTime` changes but the effect body no-ops each time (harmless churn, no functional bug); while genuinely paused, `currentTime` is frozen so `flushContinuity` identity is stable and the effect only fires once. Not reported (no observable failure).
- **`getLocalEpisodePlaybackSource` / server-local source `id` fields use `Date.now()`** (`downloaded-${Date.now()}`, `local-${Date.now()}`) rather than a monotonic counter or uuid. Two calls in the same millisecond (e.g. a double-tap) could theoretically produce the same source id, which `use-mpv-player.ts`'s `source.id === loadedSourceId.current` guard would then treat as "already loaded" and skip the state reset. Considered too speculative (no concrete double-tap-in-1ms repro path found; RN event dispatch/JS thread timing makes sub-ms double invocation implausible) to report at more than trivial severity, and didn't want to pad the list with a low-confidence item when the continuity finding is well-evidenced.
- **`PlayerCommand` type (types.ts:151-159) is exported but has zero call sites** repo-wide (only its own definition + barrel re-export). Genuine dead code but trivial/cosmetic; folded into finding #2's writeup rather than reported as a separate line item.
- Track auto-select / preference-apply hooks in `use-mpv-player.ts` (`hasAppliedDefaultTracks`, `hasAppliedPrefs` one-shot refs) — checked for reset-on-source-change correctness (both reset in the `useLayoutEffect` keyed on `source.id`, line 141-154) and for double-apply risk; found correct.
- `external-players.ts` Android-specific code paths (`ExpoExternalPlayer.open`, `intent://` templates) — confirmed gated behind `Platform.OS === "android"` checks, so on the iOS-only shipped target these are dead branches, not bugs; not reported per the "android dirs not built" scoping note.

## Coverage

Read every file in `src/lib/player/` in full (2784 lines total across 13 files, no sampling). Traced call sites into `app/(app)/(media)/player.tsx` and `app/(app)/(tabs)/(profile)/active-stream.tsx` where needed to confirm whether a suspected gap in this scope's state machine was actually reachable/observable, and into the server's `internal/debrid/client/stream.go` to confirm the debrid stream status enum has no "stopped" terminal state (read-only cross-check, not part of the audited surface). Did not audit `use-torrent-stream-controller.ts`, `watch-room.ts`, or `player.tsx`'s UI/gesture code beyond the specific sections needed to trace the two findings above — those belong to other agents' scopes (nakama/watch-room, UI/gestures, torrentstream controller) per the task's scope split. No android-only code paths audited beyond confirming they're platform-gated (iOS is the only shipped target per task brief).
