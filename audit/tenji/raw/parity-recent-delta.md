# Parity delta review — commit-by-commit, 20596e8b..HEAD (seanime-web), after 2026-07-06

Scope: `git log 20596e8b..HEAD --oneline -- seanime-web`, every non-merge commit dated
2026-07-07 or later (3f1f4663 .. 7dec652f), read via `git show <sha> -- seanime-web`.
Tenji baseline: v0.1.24 (HEAD 65f8baf, 2026-07-09).

## Commits reviewed (chronological)

- **3f1f4663** (2026-07-07) "fixes / feat: anilist rate limit countdown"
  New `rate-limit-loader.tsx`: top progress bar + toast-style pill listening on
  `WSEvents.ANILIST_RATE_LIMIT` (`anilist-rate-limit`), counts down seconds until AniList
  rate limit clears. Mounted in `main-layout.tsx` / `offline-layout.tsx`.
  Tenji: `grep -rn "ANILIST_RATE_LIMIT|rate-limit|RateLimit|rateLimit" -i` → **zero hits**
  anywhere in `src/`. Confirmed missing — no AniList rate-limit UX at all in Tenji.
  → **MISSING**. Small, self-contained (new WS listener + countdown UI). Effort S.

- **dbf3446b** (2026-07-07) "fix mpvcore preferred track selection"
  MpvCore-only bugfix: adds `selectPreferredTracks` that runs once per playback id via a new
  `mc_selectPreferredTrack` call plus explicit `player.selectTrack()` IPC calls (mpv-prism
  auto-select was unreliable). Touches only `mpv-core-player-inner.tsx`.
  Tenji already has independent equivalent logic: `findPreferredTrack` in
  `src/lib/player/player-preferences.ts`, invoked from `src/lib/player/use-mpv-player.ts:356-381`
  ("Auto-select preferred audio/subtitle tracks once tracks appear"). → **N/A** (MpvCore/Electron
  internal fix; Tenji's separately-built native equivalent already works).

- **9e337064** (2026-07-07) "fix(videocore): subtitle renderer race"
  Rewrite of `video-core-subtitles.ts` (browser `<video>` element JS/canvas subtitle renderer
  used by VideoCore, the non-Electron web fallback player) — idempotency guard, destroy()
  reset. Pure browser-DOM subtitle rendering path. → **N/A** (Tenji renders subtitles via the
  native mpv engine through expo-mpv-player, not a JS canvas renderer).

- **78dd0aeb** (2026-07-07) "fix(plugin): preserve new badge states after remounts"
  `plugin-sidebar-tray.tsx` / `plugin-tray.tsx` — plugin extension "new" badge persistence
  across remounts. Tenji has no plugin/extension tray or sidebar UI at all
  (`grep -rln plugin src` only matches generated API hook/type files, no UI component).
  → **N/A** (plugin system UI is out of Tenji's scope entirely, not a deferred item — no
  plugin management UI exists to have parity with).

- **b0db2ada** (2026-07-07) "bump versions" (playback-play-pill.tsx)
  Adds human-readable loading-state labels ("Adding torrent \"X\"", "Checking torrent...",
  "Selecting file...", "Sending stream to player...") to the floating pill, replacing raw enum
  text. Tenji already has its own independent label mapping in
  `anime-entry-torrent-stream-section.tsx` (`loadingLabel` memo). → **PORTED** (equivalent,
  independently implemented).

- **1490d0a6 / e723ac7f** (2026-07-08) "fix: nakama / fix(mediacore): subtitle preference"
  Despite the commit message, the actual web diff here is a subtitle/audio *track-language
  matching* quality improvement: new `isTrackLanguageMatch()` in `lib/helpers/language.ts`
  (tokenized fuzzy match) plugged into `mc_selectPreferredTrack` in `mpv-core.tsx`, plus support
  for a `"none"` subtitle preference value. MpvCore/VideoCore-only files touched
  (`mpv-core-player-inner.tsx`, `mpv-core.tsx`, `video-core-audio.ts`, `video-core-subtitles.ts`).
  e723ac7f additionally ships a **Dummy Debrid provider settings UI** in `debrid-settings.tsx`
  (197 new lines: provider dropdown gets a "Dummy Debrid" option gated behind
  `serverStatus.featureFlags.dummyDebrid`, plus a full profile editor — fake files/latency/
  bandwidth knobs for testing debrid flows without a real provider). This is a developer/QA
  testing tool behind a feature flag, not an end-user feature.
  → Track-language-match quality: **N/A** (Tenji's `findPreferredTrack` is a separate
  implementation; not verifying equivalence at this granularity — too speculative to flag as a
  gap). Dummy Debrid settings UI: **N/A** (dev-only testing tool, not meant for real users /
  not a candidate for a phone client).

- **e0edcaf3** (2026-07-08) "fix(namaka): don't interfere with players of peers not in watch
  party / fix(torrent): .torrent filename"
  Web diff is unrelated to its own headline (nakama fix is Go-only): adds a warning Alert to
  the Auto Downloader Rules tab ("The auto downloader is currently disabled. Enable it here.")
  plus a small `Alert` component spacing tweak. Auto-downloader itself is a KNOWN-DEFERRED item
  (rules manager) — this is a minor UX addition on top of an already-deferred surface, not a
  new distinct gap. → rolled into existing **deferred-known** (auto-downloader rules manager).

- **9f6e250c** (2026-07-08) "feat: magnet-to-library, dash-season parsing, multi-season pack
  coverage (3.9.3)"
  Multiple sub-features:
  - `sea-command-torrent-magnet.tsx`: paste-magnet quick path, downloads straight to library
    root, no anime/episode selection (magnet-to-library). Tenji: `grep -rln magnet src` only
    matches `torrent-stream-picker-sheet.tsx`'s copy-magnet-link action, no paste-to-library
    command. → confirms **deferred-known** (magnet-to-library paste UI) still missing, as expected.
  - `torrent-common-helpers.tsx` / `torrent-preview-list.tsx` / `torrent-table.tsx`: adds the
    debrid Cached/Uncached checkbox filter (`getTorrentCacheStatus`, name-flag + instant-availability
    based). This is the feature already listed as PORTED in the baseline
    (Tenji's torrent-picker Cached/Uncached filter) — confirmed present in web here, and Tenji
    already has it per baseline. → **PORTED** (already verified in baseline, no new finding).
  - dash-season parsing / multi-season pack coverage / per-user AniList collection fan-out are
    Go-backend-only (habari parsing, `seasonCovered()`, session wiring) — no web diff beyond the
    above. → N/A for this web-only delta (backend logic, transparent to any client).

- **ce4181ac** (2026-07-08) merge commit — upstream v3.9.1 conflict resolution. Diff vs first
  parent is the union of 3f1f4663/dbf3446b/9e337064/78dd0aeb/1490d0a6/e723ac7f already reviewed
  above (rate-limit-loader, plugin tray badges, dummy debrid, language.ts, video-core-subtitles
  idempotency, playback-play-pill status-clearing fix) — no *new* content beyond those. One item
  spotted only via this consolidated diff: **playback-play-pill.tsx** status-transition fix
  (from e723ac7f) — `"started"` status now clears `debridState`/`autoSelectState` instead of
  being treated the same as `"downloading"` (was causing the pill to show stale progress after
  a stream actually started). This is a web-only debrid-stream-state bugfix; Tenji has an
  entirely different, independently-built stream-state architecture
  (`ActiveStreamSessionStatus`: preparing/ready/playing in `src/lib/player/session.ts`) — no
  direct line-for-line equivalence to check, and not flagging as a specific reproducible gap
  without live-testing both. → **N/A** (internal state-machine bugfix, architectures diverged
  intentionally; not evidence of a Tenji bug).

- **2397ef32** (2026-07-10) "feat: cache loading screen artwork server-side, prefetch on entry
  page"
  `video-core-loading-screen.tsx` rewritten: fetches ani.zip fanart+clearlogo artwork via a new
  server-cached endpoint (`useAnizipArtwork` → `/api/v1/anizip-artwork/:mediaId`, 7d filecache),
  and a new `useAnizipArtworkPrefetch()` hook (called from `anime-entry-page.tsx`) that
  `new Image().src = url`-preloads the backdrop+logo into browser cache when the user opens the
  anime page — *before* they hit play. The loading screen itself now gates all visuals
  (`artworkReady = hasArtwork && backdropLoaded && (logo ? logoLoaded : true)`) so text/logo/
  gradient never pop in one after another; everything fades in together once both images are
  loaded.
  Tenji check: `grep -rln "anizip-artwork|fanart|clearlogo|AniZipArtwork" src` → **zero hits**.
  Tenji's own pre-playback loading indicator
  (`anime-entry-torrent-stream-section.tsx:142-169`) is a small inline card (ActivityIndicator +
  text + Cancel button) on the entry page — no artwork, no backdrop, no logo, no prefetch.
  Checked `player-panel.tsx` / `player-overlays.tsx` / `use-mpv-player.ts` for any full-screen
  buffering/loading overlay once the player itself is open — none found (`isBuffering` search
  turns up nothing in the player UI components). So Tenji has neither (a) the Stremio-style
  full-bleed artwork loading screen, nor (b) the server-side artwork cache + prefetch-on-entry
  behavior.
  → **MISSING**. This is a real UX gap per the task's own applicability guidance (loading-screen
  design/artwork prefetch explicitly called out as usually applicable to a native player).
  Effort M (needs: call the same `/api/v1/anizip-artwork/:id` endpoint — already exists
  server-side and is generic — plus a full-screen loading view in the player flow with
  Image prefetch via RN `Image.prefetch()`, gated fade-in).

- **7dec652f** (2026-07-10) "fix: full-fork audit — security auth-gating, crash guards,
  multi-user isolation, MpvCore watch-room sync (3.9.5)"
  Large multi-area commit; web-relevant pieces:
  - **Watch-room sync (H1)**: new `PlayerSyncControl` abstraction (`video-core-atoms.ts`) that
    lets `nakama-room-sync.ts` drive either VideoCore (DOM `<video>`) or MpvCore (native
    IPC/mpv-prism) uniformly — this is what makes watch-room sync work on MpvCore for the first
    time since v3.9 (previously dead, per memory baseline `h1-mpvcore-watchroom`). Includes
    seek detection via 2s-cooldown position-jump, teardown-emit guard, buffering-hold wiring.
    Tenji check: Tenji has its own, independently-built watch-room sync
    (`src/lib/nakama/use-watch-room-sync.ts`, 293 lines) which *already* implements the same
    concepts and more: `APPLY_SEEK_THRESHOLD`/`LOCAL_SEEK_THRESHOLD` jitter avoidance, a
    buffering-hold guard ("Buffering hold: a STALLED driver ... must not anchor the room"),
    smooth-convergence tuning, and echo-suppression via state-matching — this matches (and in
    some respects exceeds) what 7dec652f just landed for MpvCore. Per memory
    `denshi-directstream-seek-reset.md`, Tenji's seek-cooldown + buffering-hold fix already
    shipped earlier. → **N/A / already superior** — no gap; Tenji's sync predates and covers
    this fix's scope on its own native player.
  - **Debrid auto-resume for MpvCore** (`handle-debrid-reconnect.ts`): watches both
    `nativePlayer_stateAtom` and the new `mpvCore_stateAtom` so a mid-stream server restart
    resumes playback regardless of which of the *two* web player types was active. Tenji only
    has one player type, so the multi-player-bridging aspect is N/A by construction, but the
    underlying "resume debrid stream after server restart mid-play" behavior is already present
    in Tenji's own `src/lib/player/debrid-reconnect.ts` (same `droppedWhileActiveRef` /
    re-issue-once pattern). → **PORTED** (equivalent, single-player scope makes the multi-atom
    bridging moot).
  - **Loading screen reads the live debrid atom** (`video-core-loading-screen.tsx` 6-line fix):
    corrects an import so the loading screen reads the atom `handle-debrid-stream.ts` actually
    writes (was importing a dead atom from an unmounted overlay, leaving detailed debrid
    messages/torrent name permanently null on the loading screen). Purely a web-side plumbing
    bugfix, folds into the 2397ef32 loading-screen gap already reported above — not a separate
    finding.
  - **HEVC/AV1 codec-string fix** ("hex level → decimal, literal O → digit 0"): not present in
    the seanime-web diff at all (`git show 7dec652f --name-only | grep -i codec/hevc` → no file
    matches) — this is a Go-backend-only fix (internal/), transparent to every client. N/A for
    this web-scoped delta.
  - Autoselect / multi-user-isolation / robustness / security items in this commit are entirely
    Go-backend (`internal/`) — no seanime-web files touched — out of scope for a web-diff-driven
    client-parity review (server-side behavior is inherently client-agnostic).

## Summary of findings from this delta sweep

1. **MISSING** — AniList rate-limit countdown banner (3f1f4663). New, self-contained, small.
2. **MISSING** — Server-cached ani.zip artwork loading screen + prefetch-on-entry-page
   (2397ef32). Real UX gap; the task's applicability rules call this out explicitly as something
   that should port to a native player despite MpvCore being Electron-only.
3. Everything else in this commit range is either: already covered by the existing
   deferred-known list (magnet-to-library, auto-downloader), already ported via Tenji's own
   independent implementation (track auto-select, human-readable loading labels, debrid
   reconnect-resume, watch-room seek/buffering sync — Tenji's is arguably more advanced), or
   N/A (MpvCore/VideoCore-internal architecture bridging, plugin tray UI that doesn't exist in
   Tenji at all, dev-only Dummy Debrid testing tool, Go-backend-only changes).

No other net-new gaps found in this commit range beyond items 1 and 2 above.
