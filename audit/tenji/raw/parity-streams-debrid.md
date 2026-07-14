# Parity audit — Streaming flows (debrid-stream, torrent-stream, onlinestream, prewarm, loading-screen)

Scope: seanime-web (Denshi bundled UI) @ current HEAD vs seanime-tenji @ v0.1.24 (65f8baf).

## Correction to task framing
Task cited `78dd0aeb` as "badge-state preservation" for prewarm/preload badges. Verified via
`git show --stat 78dd0aeb`: that commit is `fix(plugin): preserve new badge states after remounts`,
touching only `plugin-sidebar-tray.tsx`/`plugin-tray.tsx` — an Electron plugin-tray "new" badge,
unrelated to debrid prewarm. The actually relevant commit is `c68e31fe`
(`feat(debrid/prewarm): metadata-warm next episode, drop cache on finish, mount-based badge`),
found via `git log --oneline --all | grep -i badge`. Its message explicitly states
"Tenji drops the poll too" — i.e. tenji was updated in the same commit series.

## 1. debrid-reconnect (7dec652f lineage)
- Web: `seanime-web/src/app/(main)/entry/_containers/debrid-stream/_lib/handle-debrid-reconnect.ts`
  now watches BOTH `nativePlayer_stateAtom` and `mpvCore_stateAtom` (dual-player architecture,
  MpvCore is Denshi's default since v3.9).
- Tenji: `src/lib/player/debrid-reconnect.ts` (56 lines, read in full). `useDebridReconnectResume()`
  watches `activeStreamSessionAtom?.streamMode === "debrid"` + `websocketConnectedAtom`; reissues
  `lastDebridStreamStartAtom` on reconnect. Single-player architecture (expo-mpv-player only) means
  there's only ever one player atom to watch — tenji's simpler design is a valid substitute, not a
  gap. Comment notes the resume is idempotent at the source (external-player handler in `session.ts`
  skips reload when resolved URL is unchanged).
- Verdict: **ported** (architecturally simpler, functionally equivalent).

## 2. playback-play-pill "hide when loading screen visible" (7dec652f)
- Web: `torrent-stream/playback-play-pill.tsx` — added `vc_loadingScreenVisibleAtom`; floating pill now
  also hides when the Stremio-style full-screen loading screen is showing (pill was stacking on top of
  it).
- Tenji: `src/components/features/torrentstream/anime-entry-torrent-stream-section.tsx` (288 lines,
  read in full) uses a single inline status card (blue "preparing" card w/ spinner+Cancel while
  loading → green "active" card w/ Stop once loaded) — there is no floating-pill/full-screen-loading
  duality in tenji's UI to conflict in the first place.
- Verdict: **n/a** (architecture doesn't have the failure mode this fixes).

## 3. PlayerSyncControl abstraction (video-core-atoms.ts, video-core.tsx diffs from 7dec652f)
- Web: added `PlayerSyncControl` interface + `vc_globalPlayerSyncControl` atom bridging VideoCore (DOM)
  and MpvCore (Electron/mpv-prism IPC) for watch-room sync, since Denshi now has two player backends
  needing one sync interface.
- Tenji: single player backend (expo-mpv-player), so no dual-backend bridging is needed. Tenji's own
  `src/lib/nakama/use-watch-room-sync.ts` (294 lines, read in full) talks directly to the one player
  instance. It's demonstrably MORE sophisticated than web's sync in some respects: smooth speed-nudge
  convergence for followers (`NUDGE_GAIN=0.12`, `NUDGE_MAX=0.05`, glides via `player.setSpeed()` rather
  than hard-seeking on small drift), buffering-hold heartbeat, state-matched echo guard
  (`APPLY_ECHO_WINDOW_MS=2500`), half-RTT latency compensation, forceHostTracks mirroring.
- Verdict: **n/a** for the abstraction itself (single-player architecture doesn't need it); underlying
  watch-room sync capability is **ported / ahead**.

## 4. Loading-screen status sync (a5ddaf67 / a1fb0208 lineage)
- Web: `video-core-loading-screen.tsx` — Stremio-style full-screen loading screen: anizip artwork
  (fanart backdrop + logo), gradient fallback, status text sourced from whichever of two independent
  channels (coarse `loadingState` open-steps vs detailed debrid resolve `debridMsg`) updated most
  recently (a5ddaf67 fixed a stale-precedence bug where the wrong channel could clobber a newer one).
  a1fb0208 lineage: earlier per-step loading-text fix (see memory `loading-screen-status-alignment.md`
  — this exact web bug was previously chased and fixed w/ an `openSignaled` gate).
- Tenji: `anime-entry-torrent-stream-section.tsx` bifurcates `loadingLabel` per `streamMode` at the
  source (debrid mode reads only `debridStreamState?.message`; torrent mode reads only
  `getTorrentStreamLoadingLabel(loadingState, loadingTorrentName)`) — never merges both channels for
  one session, so the "most recent wins" race that a5ddaf67 had to fix cannot occur in tenji's design.
  BUT: **the visual richness is a real, confirmed gap** — tenji's full-screen player loading state
  (`app/(app)/(media)/player.tsx` lines 974-983, read in full) is a plain black screen +
  `ActivityIndicator` + text, with no fanart/backdrop/logo/gradient at all:
  ```tsx
  if (loadingMessage && !source) {
      return (
          <View className="flex-1 bg-black items-center justify-center">
              <StatusBar hidden />
              <ActivityIndicator size="large" color="#ffffff" />
              <Text className="text-white/70 mt-4 text-base">{loadingMessage}</Text>
          </View>
      )
  }
  ```
- Verdict: staleness-race fix itself = **n/a** (impossible by construction in tenji); artwork/backdrop
  on the full-screen player loading state = **missing** (genuine, confirmed visual gap).

## 5. Prewarm / preload badges (actually c68e31fe, not 78dd0aeb)
- Web: `debrid.hooks.ts` `useDebridPrewarmStatus(enabled)` — switched from a fixed 30s poll to
  mount-fetch (RQ default staleTime 0) + invalidation-on-preload, `gcTime: 60_000`. Badge tiers:
  prewarmed (URL resolved) vs metadata-warmed ("hot").
- Tenji: `src/components/features/anime/prewarm-badge.tsx` (51 lines, read in full) — same tiering
  (orange `#c24e00` = prewarmed, red `#940a00` = hot/metadata-warmed), same
  `useDebridPrewarmStatus(enabled)` hook shared from the generated API client. Structurally matches
  web's current (mount-fetch) implementation — commit message for c68e31fe explicitly confirms tenji
  was updated in the same change.
- Verdict: **ported**.

## 6. Prewarm trigger points
- Web has three trigger points:
  (a) entry/search-page mount prewarm — `debrid-stream-page.tsx` ~L98-111, prewarms next-up episode
      on page visit.
  (b) in-playback next-episode prewarm — `video-core-playlist.tsx` `preloadNextEpisode()` ~L250-304,
      fires at ~80% progress through the current episode, sets `prewarmMetadata: true` for the
      highest-certainty next target, gated on `serverStatus?.debridSettings?.preloadNextStream`.
  (c) hover prewarm on continue-watching cards — `continue-watching-header.tsx` L266,
      `onMouseEnter` — mouse-only.
  Server-side also runs independent background prewarming (`internal/core/prewarm.go`) for
  continue-watching + airing shows regardless of any client trigger.
- Tenji: `src/components/features/torrentstream/use-debrid-prewarm.ts` (55 lines, read in full) is the
  shared hook (de-dupes via `firedRef` keyed `${mediaId}|${episodeNumber}|${aniDBEpisode}`, gated on
  `debridSettings.enabled && preloadNextStream`). Confirmed wired at BOTH trigger points:
  - entry-mount: `app/(app)/entry/anime/[id]/index.tsx` L41-46 — prewarms `resolvedEntry?.nextEpisode`
    on mount (direct equivalent of web's (a)).
  - in-playback: `app/(app)/(media)/player.tsx` L787-818 — fires at **3 seconds** into playback
    (`PRELOAD_START_SECONDS = 3`) with `prewarmMetadata: true`, gated on
    `source.nextEpisodeAction === "debridstream-auto-select"`, `prefs.autoNextEpisode`,
    `canAutoAdvance`, `nextEpisode?.aniDBEpisode`. Code comment: "was 80%, which left it only ~20% of
    runtime" — i.e. tenji deliberately fires much earlier than web's current 80% trigger, covering
    nearly the whole episode's runtime for the background resolve+download to complete.
  - Hover prewarm (c): correctly **n/a** — touch UI has no hover concept.
- **Correction to my own earlier (in-session, pre-compaction) false negative**: an initial
  `grep -rn "useDebridPrewarm\b|prewarm(" src --include="*.ts" --include="*.tsx"` (scoped only to
  `src/`) returned zero call sites and wrongly suggested the hook was dead code. Both real call sites
  live under `app/` (Expo Router screen convention), which that grep excluded. Re-run without the
  `src` path restriction confirms both usages. Reported here as ported, not missing.
- Verdict: **ported, and the in-playback trigger is arguably ahead of web's current implementation**
  (much earlier fire point covering more of the episode runtime — this is a genuine reverse-gap worth
  noting, not a parity item to action).

## 7. Onlinestream — auto-provider-cycler (use-onlinestream-auto-provider-cycler.ts, 423 lines, read in full)
- Web: `useOnlinestreamAutoProviderCycler` implements a full retry state machine
  (`TrialState {providers, providerIndex, serverIndex}`) that automatically cycles through providers
  then servers on: episode-list-error, no-episodes, episode-not-found, episode-source-error,
  no-video-sources, playback-error, playback-timeout. `PROVIDER_TIMEOUT_MS=15_000`,
  `PLAYBACK_TIMEOUT_MS=20_000`. Exposes `isTrying`, `showButton`, `tryAllProviders`, `cancel`,
  `onPlaybackError`, `onPlaybackStalled`, `onLoadedMetadata`, `onTimeUpdate` — wired into the
  onlinestream page's UI as an automatic "Try again" / cycling affordance.
- Tenji: `src/components/features/onlinestream/use-onlinestream-controller.ts` (256 lines, read in
  full) and `anime-entry-onlinestream-section.tsx` (339-341 lines, read in full) — confirmed manual-only:
  Provider `NativeSelect` dropdown, Server `Pressable` pills, Quality pills, manual "empty cache"
  button, manual "search" (manual-match) button. No retry state machine, no timeout constants, no
  auto-cycling, no "Try again" affordance anywhere in either file.
- Verdict: **missing**. Applicability: onlinestream (non-debrid HLS-based streaming) is a real,
  used feature surface in tenji (routes through the same native player, `player.tsx` L666/747/749),
  so this fully applies — not an Electron-only or browser-only concern. Impact: when a provider/server
  is flaky (common for onlinestream sources), web self-heals automatically; tenji requires the user to
  manually notice the failure and manually pick a different provider/server one at a time. Effort: M
  (state machine + timeout handling + wiring to the existing manual UI as an "auto" mode/button;
  reuses attempt/exhaustion logic conceptually parallel to web's).

## 8. Onlinestream — per-media dub + audio-track preference (commit 9d776842)
- Web: `onlinestream-page.tsx` adds `__onlinestream_dubbedPreferenceByMediaAtom` (persisted,
  atomWithStorage) and `__onlinestream_audioTrackPreferenceByMediaAtom` (persisted HLS audio-track
  choice per media), with `findPreferredAudioTrack`/`normalizeAudioTrackLanguage` alias-matching logic
  covering en/ja/fr/es/pt/de/it/ru/ko/zh.
- Tenji: `use-onlinestream-controller.ts` L72 — `dubbed` is a plain `React.useState(false)`, NOT
  persisted, in contrast to `selectedProviderAtom`/`selectedServerAtom`/`selectedQualityAtom` which
  ARE `atomWithStorage`. `grep -n "audioTrack|preferredAudio|HLS.*audio" use-onlinestream-controller.ts`
  returns nothing — no HLS audio-track selection/preference logic exists at all.
- Applicability check: onlinestream in tenji routes through the same single native `player.tsx` as
  debrid/torrent streams (confirmed: no separate onlinestream player component), so an HLS
  audio-track-preference feature is meaningful only if tenji's onlinestream flow exposes multiple HLS
  audio tracks to switch between at all — this needs source-level confirmation but the *dub* (sub vs
  dub source selection, which is a provider-level choice, not an HLS-track choice) is definitely
  applicable and definitely not persisted.
- Verdict: dub preference-by-media = **missing** (S effort — same atomWithStorage pattern as the three
  sibling atoms already in the file). HLS audio-track preference-by-media = **missing** but lower
  confidence on applicability depth; reported as missing with a note since dubbed/sub selection is
  unambiguously applicable regardless of the deeper HLS multi-track question.

## Deferred-known items in this area
None of the pre-listed deferred-known items (subtitle color/outline, magnet-to-library, auto-select
editor, auto-downloader manager, AniList upload trigger, Relations/Recommendations, Up-Next
chapter-markers, My-Lists Stats, manga Page-Fit) fall within streaming-flows scope — not re-reported.

## Summary
Most of this area is solidly ported or correctly n/a by tenji's simpler single-player architecture;
watch-room sync and next-episode prewarm are arguably ahead of web. Three genuine gaps found:
(1) player loading screen lacks artwork/backdrop, (2) onlinestream has no auto-provider-cycler,
(3) onlinestream dub/audio-track preference isn't persisted per-media.
