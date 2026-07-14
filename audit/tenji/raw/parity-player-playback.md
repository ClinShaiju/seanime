# Parity audit — Player + playback UX (video-core / mpv-core vs Tenji player)

Scope: `seanime-web/src/app/(main)/_features/video-core/`, `.../mpv-core/`, subtitle
settings, loading-screen + artwork prefetch (2397ef32), truthful loading-status
semantics. Compared against `seanime-tenji/app/(app)/(media)/player.tsx`,
`src/components/features/player/`, `src/lib/player/`, `modules/expo-mpv-player/`.

Baseline: Tenji v0.1.24 (65f8baf). Web audited at current HEAD (5a8a9b74).

## Architecture note

Denshi (Electron) runs `mpv-core` (native mpv via mpv-prism), with `video-core`
(HTML5/hls.js) as the browser-tab fallback. `VideoCoreLoadingScreen` (in
`video-core/`) is imported and rendered by `mpv-core-player-inner.tsx` too — it's
shared UI, so it IS what Denshi shows on every stream start. Tenji has its own
native mpv via `expo-mpv-player`, closest analog to `mpv-core`. `video-core`
browser-only internals (HLS.js buffering, MediaCaptions API subtitle rendering,
Chromecast, in-viewport auto-pause) are n/a; the loading-screen and status-text
semantics they share with mpv-core are NOT n/a and were checked.

## 1. Loading screen — MAJOR GAP

Web: `video-core-loading-screen.tsx` (85 lines rewritten in 2397ef32,
2026-07-10 same day as this HEAD). Renders:
- ani.zip backdrop (fanart) as a fading full-bleed background image, gated
  behind `onLoad` (`backdropLoaded`) so it doesn't pop in half-rendered
- ani.zip clearlogo, also load-gated, animated pulse; falls back to anime title
  text if no logo
- gradient fallback (`GradientBackground`) when no artwork at all
- bottom scrim gradient for legibility
- status text driven by BOTH the generic `loadingState` prop AND the live
  debrid-stream atom (`__debridstream_stateAtom`) — the debrid message wins
  when present, giving per-step truthful text ("Searching torrents...",
  "Checking torrent X", etc.)
- torrent name shown as a small caption under the status text
- artwork now server-cached (7d filecache via `/api/v1/anizip-artwork/:id`,
  `internal/api/anizip/anizip_helper.go`) and prefetched into the browser image
  cache when the anime entry page mounts (`useAnizipArtworkPrefetch`, called
  from `anime-entry-page.tsx`), so it's instant by the time the loading screen
  mounts.

Tenji: `app/(app)/(media)/player.tsx` lines 974-983 — a bare early-return:
```
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
No backdrop, no logo, no gradient fallback, no torrent-name subtext, plain
black screen + native spinner + one line of text. Confirmed no alternate
loading-screen component exists anywhere in `src/` (`find -iname "*loading*screen*"`
= empty) and `anizip` is only referenced for episode-title mapping
(`fetchAniZipMapping` in `player-panel.tsx`), never for artwork.

The STATUS TEXT TRUTHFULNESS mechanism, however, IS already ported and matches
web's granularity — Tenji's `session.ts` has its own
`TorrentStreamLoadingState` state machine (`SEARCHING_TORRENTS`,
`CHECKING_TORRENT`, `ADDING_TORRENT`, `SELECTING_FILE`, `STARTING_SERVER`,
`SENDING_STREAM_TO_MEDIA_PLAYER`) driving `getTorrentStreamLoadingLabel()`,
consuming the same backend WS step protocol as web's `__debridstream_stateAtom`.
`torrentStreamLoadingTorrentNameAtom` also exists and is used pre-navigation
in `anime-entry-torrent-stream-section.tsx`, but is NOT threaded into the
player's loading screen — so torrent-name display is a partial regression
even relative to Tenji's own data availability.

Verdict: partial. The *substance* (truthful, per-step status text) is ported;
the *visual richness* (artwork/backdrop/logo, torrent-name caption, load-gated
fade-in) is entirely missing. Given today's web commit specifically targeted
this UX ("no more jarring text-then-image pop-in"), and Tenji already has all
the plumbing (loading state, anizip fetch elsewhere, torrent name atom), this
is a straightforward-ish port (server endpoint already exists and is generic —
just needs an RN screen + `Image` prefetch). Effort M.

## 2. openSignaled / re-open-thrash gating (mem: loading-screen-status-alignment)

Grepped `openSignaled` / `restartMs` across the repo — only found in the Go
backend (`internal/directstream/{stream,manager}.go`), not in seanime-web
TS/TSX. This was a server-side per-step re-open guard for the WS protocol
(fixed in the a1fb0208 regression), not a client concept — so there's nothing
web-side to port; both Denshi and Tenji consume the same already-fixed
backend protocol. n/a as a distinct client finding (folded into item 1's
"status text truthfulness" which is confirmed already correct).

## 3. Subtitle style customization (color / outline / shadow / font) — DEFERRED-KNOWN, confirmed still missing

Web: `mpv-core-settings-menu.tsx` "Subtitle Styles" section — Font (family),
Font Size, Text Color, Outline (width + color), Shadow (mode + color), all
via `mpvSettings.subtitleCustomization`, sent to mpv via native setter.

Tenji: `player-panel.tsx` / `use-mpv-player.ts` expose `setSubtitleFontSize`,
`setSubtitleDelay`, `setSubtitlePosition`, `setSubtitleMarginY` — no color,
outline, shadow, or font-family setters anywhere in
`modules/expo-mpv-player/src/*.ts*` (grepped, no matches). Matches the
known-deferred item exactly ("needs a native mpv setter in expo-mpv-player").
Confirmed still missing, still applicable. status=deferred-known.

## 4. Seek-bar scrub thumbnail preview — NEW GAP

Web: `video-core-preview.ts` (`VideoCorePreviewManager`) captures video frames
via a hidden `<video>` + canvas at 4s intervals (throttled, prefetched ±10
segments), independent of the actual mpv playback — used by
`mpv-core-time-range.tsx` for BOTH hls and direct (mkv/torrent/debrid)
sources (`playbackType !== "onlinestream"` still gets thumbnails). Hovering /
dragging the seek bar shows a real frame thumbnail at that timestamp.

Tenji: `player-overlays.tsx` `SwipeSeekOverlay` and the seek-bar drag handler
in `player.tsx` (`getSeekSnappedTime`, `seekingDisplay`) show only numeric
time + chapter title — no thumbnail image anywhere (grepped `player-panel.tsx`,
`player-overlays.tsx`, `player.tsx` for "thumbnail"/"preview" — no hits beyond
generic use).

This is a real, applicable UX gap (scrub thumbnails are standard on mobile
video players too), but nontrivial to port: web's approach re-decodes the
stream via a second hidden HTML5 video element, which has no native RN
analog — would need either a native frame-extraction API in
`expo-mpv-player` or a sprite-sheet/thumbnail-track from the server. Effort L.

## 5. Global playlist / queue feature — NEW GAP (not in known-deferred list)

Web: `video-core-playlist.tsx` (472 lines) + `_features/playlists/` (editor
modal, list modal, `global-playlist-manager.tsx`) — a full user-curated
cross-anime episode queue (`Anime_Playlist`, `Anime_PlaylistEpisode` API
types), with in-player next/previous-in-playlist buttons
(`VideoCoreNextButton`/`VideoCorePreviousButton`) wired to
`usePlaylistManager()`, distinct from ordinary "next episode" autoplay.

Tenji: `src/api/hooks/playlist.hooks.ts` and the generated types exist
(codegen'd from the same backend), but grepping `src/` and `app/` for
"playlist"/"Playlist" outside the generated API layer returns nothing — no
editor UI, no in-player queue controls, no playlist list screen. Fully
missing UI despite the API client being generated. Effort L (full CRUD editor
+ in-player queue UI + websocket sync with `global-playlist-manager`'s
cross-tab reconciliation logic).

## 6. Chapter-marker display settings — minor gap

Web: `mpv-core-settings-menu.tsx` "Player Appearance" section has two toggles:
"Show Chapter Markers" and "Highlight OP/ED Chapters" (color-codes OP/ED
segments differently on the seek bar).

Tenji: chapter markers are always rendered as generic ticks in `player.tsx`
(`chapterMarkers` memo, `player-controls.tsx`) — no visibility toggle, no
OP/ED-specific highlight color. This is distinct from (and smaller than) the
already-known-deferred "Up-Next chapter-marker toggles" item (which is about
the continue-watching Up-Next card's chapter markers, a different surface) —
confirmed by checking `watch-rooms-sheet.tsx` and `nakama` files have no
chapter toggle either. Folded into a small "Player Appearance" partial finding.

## 7. In-player watch-party chat — NEW GAP

Web: `mpv-core-watch-party-chat.tsx` wraps `NakamaWatchPartyChat` (from
`_features/nakama/nakama-watch-party-chat.tsx`) as a menu inside the player
controls — text chat between room participants, unread-count badge,
minimize/maximize, toast on new message.

Tenji: `src/lib/nakama/use-watch-room-sync.ts` implements the sync protocol
(play/pause/seek/force-tracks/autoskip vote) consumed by `player.tsx`, and
`src/components/features/nakama/watch-rooms-sheet.tsx` handles room
management — but grepping for chat/message UI in the nakama or player dirs
returns nothing. No text-chat surface anywhere in Tenji. This may overlap
with a separate nakama-room-focused audit area — flagging here since the
component lives inside `mpv-core/` and is part of in-player UX, but noting
possible dup coverage. Effort M-L (chat UI + unread state, backend already
speaks the same nakama protocol).

## 8. Confirmed n/a (Electron/desktop-specific, correctly out of scope)

- `mpv-core-cast-button.tsx` — Chromecast via `window.electron.cast`, desktop
  IPC only. No AirPlay equivalent built in Tenji either (grepped, zero hits) —
  genuinely absent capability, not a hidden port. n/a (could be a *new*
  AirPlay feature idea, but that's out of scope for parity).
- `mpv-core-floating-buttons.tsx` (browser mini-player enter/exit/expand) —
  superseded by Tenji's native iOS Picture-in-Picture (`state.isPiPActive`,
  `handleStartPiP`), which is a better-native equivalent already ported.
- Anime4K / custom GLSL shaders, debanding (`mpv-core-settings-menu.tsx`
  "Shaders" section, `video-core-anime-4k*.ts`) — native mpv-prism/libplacebo
  shader pipeline, no equivalent surface in `expo-mpv-player` types (grepped,
  zero "shader" hits). Genuinely desktop-GPU-only.
- Screenshot-to-directory (`mpv-core-screenshot-prompt.tsx`,
  `video-core-screenshot.ts`) — Electron filesystem directory picker +
  server-side screenshot dir setting. No Photos-library-save equivalent exists
  in Tenji; this is a real capability gap but tied to a desktop-specific
  settings flow (screenshotDir), not a straightforward mobile port target.
  n/a for this parity pass (flag separately as a potential new feature if
  desired).
- `mpv-core-stats.tsx` (playback diagnostics overlay: codec, resolution,
  fps, bitrate, frame drops, cache) — `modules/expo-mpv-player/src/MpvPlayer.types.ts`
  exposes only `codec`/`fps` on tracks, no bitrate/frame-drop/diagnostics
  fields, so this can't be ported without extending the native module first.
  Judged legitimately low-priority for a phone client (debugging tool, not
  end-user UX) — n/a for now, but noted as blocked-on-native-module rather
  than "doesn't apply in principle."
- `video-core-in-sight.tsx` (pause-when-scrolled-out-of-view for embedded
  browser players) — n/a, Tenji's player is always full-screen native.
- `video-core-ios-fullscreen-subtitles.ts` (Safari-fullscreen subtitle quirks)
  — n/a as literal code; Tenji has its OWN native equivalent already ported
  (`syncIosSubtitleCropCompensation` in `player.tsx`, handles ASS vs
  text-subtitle margin/position compensation during pinch-zoom fill mode) —
  same UX intent, different (native) implementation. Counted as ported.
- `video-core-media-session.ts` (browser MediaSession API for lock-screen
  controls) — Tenji already passes `nowPlayingMetadata` natively to
  `MpvPlayerView`, i.e. native Control Center / lock-screen integration.
  Ported (native, better implementation).
- `video-core-mobile-gestures.ts`, `video-core-pip.ts`, `video-core-fullscreen.ts`
  — Tenji has its own native, more extensive gesture set (`use-double-tap-seek`,
  `use-swipe-seek`, `use-side-adjust`, `use-player-gestures`, native PiP,
  `use-landscape-orientation-lock`). Ported, arguably superior.
- `video-core-pgs-renderer.ts` (image-subtitle rendering for the browser
  HTML5-video fallback path) — n/a; Tenji plays via real native mpv (like
  Denshi's mpv-core path), which renders PGS/VobSub natively without a
  JS-side renderer.

## Already-verified-ported (per baseline, spot-checked cheaply)

- Wyzie external-subtitle search + API key (`wyzieApiKey`,
  `onAddExternalSubtitle` wired in `PlayerPanelOverlay` props in `player.tsx`)
  — present, matches web's subtitle-menu wyzie integration.
- Auto-next-episode, auto-skip OP/ED, playback speed, subtitle/audio delay —
  all present via `usePlayerPreferences` + `use-mpv-player.ts`.
- Debrid stream prewarm / next-episode preload — present
  (`useDebridPrewarm`, `prewarmNextDebridEpisode` effect in `player.tsx`),
  matches the "preload next episode" server setting semantics described in
  memory `debrid-preload-prewarm.md`.
