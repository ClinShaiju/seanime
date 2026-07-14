# Tenji ↔ Denshi/Web UI Parity Report

**Baselines:** `seanime-web` @ `5a8a9b74` (v3.9.5 HEAD) · `seanime-tenji` @ `65f8baf` (v0.1.24) · audited 2026-07-10/11.

**Post-dedup totals:** 16 missing · 6 partial · 7 deferred-known · 57 ported · 25 n/a · 0 dropped.

## TL;DR — gaps worth acting on, ranked

1. **Watch-room follower seek-cooldown throttle (S)** — Tenji followers hard-seek on *every* 1s heartbeat once drift exceeds 0.6s, with no cooldown: each seek resets the in-flight directstream byte-range → rebuffer → more drift → next seek. This is exactly the thrash loop web fixed in `159a4efe`; Tenji ships the pre-fix behavior. One-line fix mirroring `nakama-room-sync.ts` (`SEEK_COOLDOWN_MS=2500` + `lastHardSeekRef`).
2. **Loading-screen artwork (M)** — flagged independently by 4 of 10 areas: web/Denshi now shows a Stremio-style server-cached ani.zip backdrop + clearlogo with gated fade-in and entry-page prefetch (`2397ef32`; endpoint `GET /api/v1/anizip-artwork/:id` is live and client-agnostic); Tenji shows a bare spinner. The status-text machinery is already ported — only the visual layer is missing.
3. **Debrid cache-flag detection for the Cached/Uncached picker filter (S)** — Tenji's `isCached()` is infoHash-availability only; on RealDebrid/AllDebrid (whose instant-availability APIs are dead) the filter chips never even appear and per-card badges are always false. Port web's `getTorrentCacheStatus()` name-flag heuristic (bolt/hourglass markers, `rd+`/`tb+`/`ad+` prefixes) — pure string parsing.
4. **AniList rate-limit countdown banner (S)** — the server already broadcasts `anilist-rate-limit` WS events to every client; Tenji's WS router silently drops them, so rate-limited syncs look like silent hangs. One router case + a countdown pill.
5. **Onlinestream auto provider/server retry-cycling (M)** — web self-heals flaky providers via a trial state machine (15s provider / 20s playback timeouts); Tenji is manual-retry only.
6. Quick wins behind those: onlinestream dub-preference persistence (S, mechanical), entry-header context bucket (studio/rankings/trailer/links/airing countdown, S), Library/Manga genre chips (S — component and genre derivation already exist unused), My-Lists adult filter (S), entry silence toggle + schedule "Silenced" section (S — hooks already synced, unused).
7. Big-ticket items to schedule deliberately: cross-anime playlists (L), scrub thumbnails (L, needs native module or server sprites), watch-party chat (M), admin user management (M), AniList connect/disconnect (M).

## Method

- 10 area agents (types-endpoints, player-playback, entry-screen, library-lists, discover-schedule-search, settings-profile, nakama, manga, streams-debrid, recent-commit-delta safety net over the same window).
- Every claimed **gap** was adversarially re-verified in both directions: (a) the web feature is real and live at the cited ref; (b) Tenji genuinely lacks it, including sweeps for renamed reimplementations; (c) it actually applies to an iOS client. Verification notes are condensed per item below.
- **Ported** and **n/a** claims come from the area agents' primary pass and were *not* re-run through the adversarial verifier; items with weaker evidence are flagged UNVERIFIED inline.
- The recent-delta pass re-discovered several area findings; duplicates are merged here, keeping the richer refs. Raw per-area notes: `raw/parity-*.md`.

## Real gaps

### Missing

#### M1. Watch-room follower seek-cooldown throttle
- **Web:** `seanime-web/src/app/(main)/_features/nakama/nakama-room-sync.ts` — `SEEK_COOLDOWN_MS=2500` (L76), `lastHardSeekRef` (L137), gated heartbeat branch (L448), ref stamped on both the discrete-seek (L445) and heartbeat hard-seek (L454) branches. Commit `159a4efe`.
- **Tenji:** `src/lib/nakama/use-watch-room-sync.ts:254` — ungated `drift > HARD_SEEK_DRIFT` heartbeat seek; no cooldown ref anywhere (repo sweep found only unrelated debounces; the echo suppression gates outgoing broadcasts, not incoming apply-side seeks).
- **Impact:** a follower >0.6s behind (any rebuffer) re-seeks every 1s heartbeat; each seek cancels the in-flight directstream range → self-sustaining rebuffer/thrash loop. Tenji followers join via `externalPlayerLink`, i.e. the same directstream proxy — the failure mode is identical to pre-fix Denshi.
- **Effort:** S. **Verified:** both files read line-by-line; git log confirms `159a4efe` was never ported (Tenji's `9f8656d` ports only the adjacent `703e010c`). UNVERIFIED side note: Tenji's `joinStream` omits web's `directCdnCapable` field — separate check for the CDN-handoff owner.

#### M2. Global cross-anime playlist/queue
- **Web:** `_features/playlists/` (545L CRUD editor, list modal, 477L `global-playlist-manager.tsx` with `WSEvents.PLAYLIST` server-orchestrated sequential playback), mounted app-wide in `main-layout.tsx:58-59`; in-player next/prev wired via `usePlaylistManager()` (`video-core-playlist.tsx`).
- **Tenji:** `src/api/hooks/playlist.hooks.ts` exists with **zero importers**; no UI, no WS handling; "queue"/"up next" hits are different features (download queue, continue-watching sort).
- **Impact:** cannot build or play a custom cross-anime episode queue at all; generated API layer is dead code today.
- **Effort:** L. **Verified:** both directions, incl. renamed-reimplementation sweep.

#### M3. In-player watch-party text chat
- **Web:** `mpv-core-watch-party-chat.tsx` (unread badge, minimize, new-message toast) wrapping `nakama-watch-party-chat.tsx` (message atoms, incoming-WS listener, send input, host styling).
- **Tenji:** only artifact is an unused `useNakamaSendChatMessage` (`nakama.hooks.ts:106`); no chat UI and no incoming-message WS listener; the sync stack (`use-watch-room-sync.ts`) covers playback only.
- **Impact:** room members can sync playback but not chat. **Effort:** M. **Verified:** exhaustive chat/message sweep; other hits were torrent-picker comment icons.

#### M4. AniList rate-limit countdown banner
- **Web:** `_features/rate-limit-loader.tsx` (commit `3f1f4663`), listens for `WSEvents.ANILIST_RATE_LIMIT` (`waitSeconds`), mounted in main + offline layouts; the server emit is a **global** broadcast (`internal/api/anilist/client.go:547`), so Tenji already receives the event.
- **Tenji:** `src/api/components/websocket-event-router.ts` has no `anilist-rate-limit` case — the event is dropped at the default branch; zero rate-limit UI anywhere (manga downloader's 429 backoff is internal, no UI).
- **Impact:** when AniList rate-limits the server, list/collection syncs appear to hang with no explanation. **Effort:** S — one router case + a countdown pill, same pattern as existing WS banners.

#### M5. Onlinestream automatic provider/server retry-cycling
- **Web:** `onlinestream/_lib/use-onlinestream-auto-provider-cycler.ts` (423L TrialState machine; cycles servers then providers across 7 failure classes; `PROVIDER_TIMEOUT_MS=15000`, `PLAYBACK_TIMEOUT_MS=20000`; success = 1s of playback).
- **Tenji:** `use-onlinestream-controller.ts` is manual-only; `onNativeError` just flips status to "error"; `source-resolver.ts` emits a single source with no fallback chain. Repo-wide sweep for cycler/failover/auto-switch under any name: negative.
- **Impact:** flaky providers/servers (common) require the user to notice and manually try each option one at a time. **Effort:** M — state machine over APIs Tenji already calls, reusing its manual selection UI.

#### M6. Onlinestream per-media persisted dub preference
- **Web:** `__onlinestream_dubbedPreferenceByMediaAtom` (`onlinestream/_lib/onlinestream.atoms.ts:12-17`, persisted per media id; consumed in `onlinestream-page.tsx:270-287`; commit `9d776842`).
- **Tenji:** `use-onlinestream-controller.ts:72` is plain `React.useState(false)` — while the sibling provider/server/quality prefs **in the same file** are `atomWithStorage`.
- **Impact:** dub/sub re-selected on every title open. **Effort:** S — mechanical, same pattern as the three sibling atoms.

#### M7. Entry header context bucket (studio, rankings, trailer, AniDB/MAL links, next-airing countdown)
- **Web:** `entry/_components/meta-section.tsx` — `AnimeEntryStudio` (L151-161), `AnimeEntryRankings` (L172), `TrailerModal` (L131-136), AniList link (L117-119), `NextAiringEpisode` (L238); AniDB/MAL live in the entry dropdown it renders at L195.
- **Tenji:** `media-entry-header.tsx` shows title/date/season/status/score/top-3-genres only; the Info tab adds description/characters/relations/recs but none of these; `nextAiringEpisode` is used only for progress math, never rendered. Sole sliver: an AniList `siteUrl` link in the card long-press quick-info sheet (different surface).
- **Impact:** meaningfully less at-a-glance context than the web/Denshi header. **Effort:** S (bucketed — all are small `Linking.openURL`-feasible additions; split if prioritizing individually).

#### M8. Genre selector chips on the Library and Manga tabs
- **Web:** `library-view.tsx` GenreSelector (gated on theme setting + >2 entries), `detailed-library-view.tsx`, `manga-library-view.tsx` (L155/193).
- **Tenji:** `src/components/shared/media-genre-selector.tsx` exists but is imported **only by Discover**; Library and Manga tabs filter by title only. Bonus: `use-anime-library-collection.ts:36-40,207` already computes `libraryGenres` (zero consumers) and `filtering.ts:204-207,302-305` already has the `params.genre` match logic — this is wiring, not building.
- **Impact:** no quick genre browsing on the home tabs. **Effort:** S — lowest-effort new finding.

#### M9. Entry silence toggle + Schedule "Silenced episodes" section (merged entry-screen + schedule findings)
- **Web:** `anime-entry-silence-toggle.tsx` (48L bell IconButton on Get/Toggle silence-status hooks); `schedule/_components/missing-episodes.tsx` "Silenced episodes" accordion (LuBellOff) fed by the `silencedEpisodes` split in `handle-missing-episodes.ts`.
- **Tenji:** hooks already hand-synced and unused (`anime_entries.hooks.ts:99-120`); `types.ts:1854` already carries `Anime_MissingEpisodes.silencedEpisodes`; the schedule screen reads only `missingData?.episodes`. Pure UI gap — cheaper than originally scoped.
- **Impact:** perpetually-behind or dropped titles clutter the Missing Episodes row forever with no way to mute them, and list-sync notification spam can't be silenced per-title.
- **Effort:** S. **Verified:** alternate-name sweeps (mute/BellOff/hide) negative; only false positives.

#### M10. My-Lists adult-only filter switch
- **Web:** `anilist-collection-lists.tsx:416-424` — Adult Switch on `params.isAdult`, gated by `serverStatus.settings.anilist.enableAdultContent`.
- **Tenji:** `collection-filter-sheet.tsx` (213L, fully read) has Sort/Format/Season/Year/Status/Genres/Tags only; `CollectionParams.isAdult` exists and is honored by the filter engine but nothing in the My-Lists UI can set it. The identical gated-toggle pattern already ships in the discover search sheet (`search-filter-sheet.tsx:233-237`).
- **Impact:** low/niche — users with adult content enabled can't isolate/exclude it in My-Lists. **Effort:** S — one gated Switch row.

#### M11. Anime library "Show unwatched only" shelf toggle
- **Web:** `library-collection.tsx:133-151` — DropdownMenu on the CURRENT shelf toggling `params.continueWatchingOnly` (field: `filtering.ts:113-126`; filter: L405-407; auto-reset guard in `handle-library-collection.ts:139-145`).
- **Tenji:** `src/lib/utils/filtering.ts:91-101` CollectionParams lacks the field (zero repo hits under any alias); the Library tab renders fixed shelves with no per-shelf control. Adjacent features (unwatched-count badges, Up-next boost) are different things.
- **Impact:** large currently-watching lists can't hide caught-up entries. **Effort:** M — CollectionParams field + shelf-builder threading + a menu affordance.

#### M12. Manga library "Unread chapters only" shelf toggle
- **Web:** `manga-library-view.tsx:302-355` — same pattern for `params.unreadOnly` (filter: `filtering.ts:468-474`; persisted atom).
- **Tenji:** no `unreadOnly` in CollectionParams; manga tab shelves are built raw from the collection; the repo's only `unreadOnly` is the per-entry chapter-list switch in `manga-entry-chapters-view.tsx:95` (different feature). Unread-count plumbing the filter needs already exists.
- **Impact:** mirrors M11 for manga. **Effort:** M — shares the CollectionParams work with M11 if done together.

#### M13. Lock-files toggle
- **Web:** `toggle-lock-files-button.tsx` (`useAnimeEntryBulkAction` "toggle-lock"), rendered in `meta-section.tsx:202` plus library explorer/cards/bulk modal.
- **Tenji:** the hook is synced (`anime_entries.hooks.ts:36`) with zero UI callers; lock greps hit only unrelated locks (screen lock, room passwords).
- **Impact:** can't protect an entry's files from server-side library management from the phone. **Effort:** S; low priority for a thin client.

#### M14. Admin user management (list/add/delete server accounts)
- **Web:** `settings/_containers/users-settings.tsx` (useListUsers, useRegisterUser, `DELETE /api/v1/user/{id}`), admin-only tab in `settings/page.tsx:1022` (gate at L163).
- **Tenji:** `user/list` + `user/register` exist only in generated stubs (`endpoints.ts:2487,2492`); `user-auth.hooks.ts` covers login/change-password/save-debrid/logout only; role usage is display-only (`profile/index.tsx:288-300`).
- **Impact:** a phone-only admin on a multi-user server must fall back to the web UI for basic account administration. **Effort:** M — CRUD screen reusing ProfileMenuSection + account.tsx form patterns.

#### M15. AniList connect/disconnect within a profile (web's Integrations tab)
- **Web:** `integrations-settings.tsx` — Connect opens the token-paste login modal (`/auth/callback` → per-user `/auth/login`); Disconnect confirm-dialogs into `useLogout`; gated on `user.isSimulated`.
- **Tenji:** `useLogin`/`useLogout` defined in `auth.hooks.ts` with zero call sites; onboarding (`set-server-url.tsx`) is server-URL + password only; `account.tsx` has no AniList section; the profile tab's "Sign out" is the Seanime profile-session logout, not an AniList unlink.
- **Impact:** functional, not cosmetic — a profile without a linked AniList account (fresh non-admin user, expired token) cannot link/relink from Tenji at all, and list sync depends on it.
- **Effort:** M — the web flow is token-paste (no OAuth webview needed) and the hooks already exist.

#### M16. Seek-bar scrub thumbnail preview
- **Web:** `video-core-preview.ts` VideoCorePreviewManager (hidden dummy video + OffscreenCanvas webp capture at 4s segments, prefetch queue), consumed by both `video-core-time-range.tsx` and `mpv-core-time-range.tsx` (hls + direct/mkv) — live in the Denshi player.
- **Tenji:** seek feedback is numeric time + chapter title only (`player.tsx` seekingDisplay/getSeekSnappedTime, `player-overlays.tsx` SwipeSeekOverlay). The only frame-grab primitive is an unused, unexposed, Android-only `MPVLib.grabThumbnail` declaration.
- **Impact:** no visual scrubbing — harder to seek to a specific scene. **Effort:** L — web's hidden-video technique has no RN analog; needs a native frame-extraction API in expo-mpv-player or server-generated thumbnail sprites.

### Partial

#### P1. Loading-screen artwork (merged from 4 areas + the anizip-artwork endpoint item)
- **Web:** `video-core-loading-screen.tsx` rewritten in `2397ef32` (refined `7dec652f`): server-cached ani.zip fanart backdrop + clearlogo via new `GET /api/v1/anizip-artwork/:id` (`routes.go:402` → `HandleGetAnizipArtwork`, 7-day filecache), load-gated fade-in (backdrop **and** logo `onLoad` before reveal), animated gradient fallback, torrent-name caption, `useAnizipArtworkPrefetch` called from `anime-entry-page.tsx:126`. Rendered by both the browser player and MpvCore (Denshi). Note: the hooks/types are hand-authored on web — codegen will **never** surface this endpoint, so the port needs a small hand-rolled typed fetch.
- **Tenji, ported half:** the truthful per-step status machinery matches web's granularity (`session.ts` `getTorrentStreamLoadingLabel`:402, torrent-name atom :215) — but it feeds the entry-page banner, not the player; the player loading branch (`player.tsx:974-983`) is a bare black View + ActivityIndicator + text, and `playerLoadingMessageAtom` is effectively vestigial (only ever set to null).
- **Tenji, missing half:** artwork fetch/prefetch (zero `anizip-artwork` hits; the only ani.zip use is subtitle ID-mapping in `subtitle-search.ts`), backdrop/logo/gradient composition, load gate, in-player torrent-name caption. The `artworkUri` in `use-mpv-player.ts` is lock-screen Now Playing metadata, unrelated.
- **Impact:** every stream start is visibly plainer than Denshi. **Effort:** M — endpoint reusable as-is; expo-image provides prefetch + `onLoad` gating; LinearGradient already used elsewhere. Shipped ~15h before this audit → expected absence, not a regression.

#### P2. Debrid Cached/Uncached torrent filter accuracy (RD/AD)
- **Web:** `torrent-common-helpers.tsx` `getTorrentCacheStatus()` (commit `9f6e250c`) — aggregator name-flag markers (bolt/hourglass/down-arrow, `rd+`/`tb+`/`ad+` service prefixes, "`<code> download`" uncached) + infoHash availability, tri-state, specifically because RD/AD instant-availability APIs are unreliable (mirrors backend `cache_flags.go`).
- **Tenji:** chips + wiring present (`torrent-stream-picker-sheet.tsx:769-771`), but `isCached()` (L501-504) is infoHash-only **and** the chips are gated on `cachedCount > 0` (L510) — so on RD/AD (empty availability map) the filter UI never appears and per-card badges are always false; tri-state collapsed to binary. No name-flag logic anywhere (exhaustive marker/prefix sweep).
- **Impact:** worse than originally filed — the feature is effectively invisible on RD/AD. **Effort:** S — pure string parsing on names Tenji already has.

#### P3. Onlinestream per-media HLS audio-track preference
- **Web:** commit `9d776842` — per-media persisted track pick with auto-capture of the user's manual change + language-alias matching cascade (`onlinestream-page.tsx:93-250`, atom in `onlinestream.atoms.ts:19`).
- **Tenji has a global layer the area agent missed:** `player-preferences.ts` `preferredAudioLanguages` (editable tag list, default jpn/ja/japanese) + `findPreferredTrack`, auto-applied once per load (`use-mpv-player.ts:358-387`), with a working track picker (`player-panel.tsx:250,439-447`) — covers onlinestream HLS.
- **Genuinely missing:** per-media persistence and auto-capture — `setAudioTrack` (`use-mpv-player.ts:426`) persists nothing, so a per-show choice is forgotten and the global default re-wins on next open.
- **Effort:** M — slots naturally into `setAudioTrack` + the auto-select effect and would benefit local/debrid playback too.

#### P4. Torrent search → download-to-library workflow
- Originally filed as wholly absent/deferred — verification found Tenji **reimplements it** as `server-download-modal.tsx` ("Download on Server") + `TorrentStreamPickerSheet mode="download"`: ports web's destination logic (library root + sanitized folder, editable path), "Download Missing" smart-select, "Download Full" for batches, per-file selection, torrent-client + debrid dispatch.
- **Real deltas vs web (`torrent-search-container.tsx` + `torrent-download-modal.tsx`):** (1) the ONLY trigger is gated `isConnected && isLocalServer` (`anime-entry-action-bar.tsx:74`) → unreachable on a remote server, the primary real deployment, despite the modal containing remote-server copy — likely an over-tight gate worth loosening first; (2) one torrent at a time vs web's multi-select; (3) batch file-selection picks exactly one file vs arbitrary subsets; (4) raw text destination vs server directory browser; (5) already-on-server episodes filtered out, so no re-download/replace path.
- **Effort:** gate fix S; fuller parity M/L. Magnet paste-in remains a separate deferred item (D4).

#### P5. Metadata manager + bulk file actions
- **Web:** `anime-entry-dropdown-menu.tsx` → AnimeEntryMetadataManager (filler data, metadata parent) + Download/Unmatch/Delete "some files" bulk modals.
- **Tenji:** the download-to-device goal is covered by its own `DownloadEpisodesModal`; metadata manager, unmatch, and server-file bulk delete are genuinely absent (hooks synced in `localfiles.hooks.ts`/`metadata.hooks.ts`, zero consumers; the profile "Resolve Unmatched" screen is the inverse operation). Open-directory/external-player items in the same web dropdown are correctly n/a.
- **Impact:** cover/episode-image fixes, mismatch repair, and server-file cleanup stay web-only. **Effort:** L.

#### P6. Player Appearance toggles (chapter markers / OP-ED highlight)
- **Web:** `mpv-core-settings-menu.tsx:573-595` — "Show Chapter Markers" + "Highlight OP/ED Chapters" switches; markers hidden on mobile viewports by web itself.
- **Tenji already renders the OP/ED highlight** always-on (`player-controls.tsx:111-115,286` — same OP/ED regex as web, same blue-300 ~half-alpha tint) on a chapter-segmented seek bar; only the two preference toggles are missing (`player-preferences.ts` has `autoSkipOpEd` only). The original claim's "generic ticks" description was stale — the passed `chapterMarkers` memo is dead code; the segmented track IS the chapter visualization.
- **Impact:** small — missing user control over always-on rendering, not missing rendering. **Effort:** S.

### Deferred-known (on the v0.1.24 deferred list; re-confirmed still absent and still applicable this pass)

#### D1. Subtitle style customization (color, outline, shadow, font family) — L
Web: `mpv-core-settings-menu.tsx` "Subtitle Styles" (8 colors, outline width/color, shadow depth/opacity/color, font family; mirrored in video-core). Tenji: native module exposes only track/delay/fontSize/visibility/position/scale/margins (`MpvPlayer.types.ts:130-141`); no styling panel. Blocker unchanged: expose `sub-color`/`sub-border-size`/`sub-shadow-offset`/`sub-back-color`/`sub-font` setters through expo-mpv-player (`MPVLayerRenderer.swift` already does `setProperty` for `sub-font-size`, so the mpv side works on iOS). Scope is slightly larger than the deferred entry implied (shadow + font family too, same blocker).

#### D2. Auto-downloader rules (manager + per-entry rule button; merged) — L (M for just the entry shortcut)
Web: standalone `/auto-downloader` page (rule CRUD, batch rules, profiles, queue, settings — note: standalone page, not an admin-settings tab) + `AnimeAutoDownloaderButton` in the entry header (`meta-section.tsx:197`) listing this anime's rules + New Rule. Tenji: `auto_downloader.hooks.ts` synced with zero consumers; profile download screens are unrelated (device prefs / active downloads). Fully feasible on iOS — rules are server-side config.

#### D3. Auto-select debrid profile editor — L
Web: `user-debrid-settings.tsx:113-125` AutoSelectProfileButton (only when `useServerAutoSelect=false`) → full editor (resolutions, release groups, language/codec/source ranking, min seeders, size limits, batch/best-release prefs). Tenji: `account.tsx` has only the on/off toggle; profile hooks synced in `torrent_search.hooks.ts`, unused; no reimplementation (distinctive-field sweep negative).

#### D4. Magnet-to-library paste UI — S
Web: `_features/sea-command/sea-command-torrent-magnet.tsx` (commit `9f6e250c`; original citation said `_components/` — corrected): `/magnet` command → "Download to library (auto-detect)", library root, no picker. Tenji: the only magnet usage copies OUT to clipboard (`torrent-stream-picker-sheet.tsx:1405-1412`); both mutation hooks already synced. Clipboard magnet paste is natural on iOS.

#### D5. My-Lists Stats tab (AniList analytics) — L
Web: `anilist-stats.tsx` (795L: score distribution, status donut, genre/format/studio rankings, year charts) as 3rd StaticTabs tab. Tenji: `useGetAniListStats` synced (`anilist.hooks.ts:312`), zero call sites; `my-lists.tsx` TypeToggle offers anime/manga only. A condensed mobile stats view is standard (AniList's own app), so applicable — just large.

#### D6. Manga Page Fit modes (Contain/Overflow/Cover/True size) — M
Web: `MANGA_PAGE_FIT_OPTIONS` per-media RadioGroup, applied as CSS classes in both readers. Tenji: no `pageFit` in `MangaReaderSettings`; hardcodes web's mobile defaults (`contentFit="contain"` paged, full-width long strip in `manga-reader-layout.ts`). Pinch-zoom is a transient gesture, not a renamed fit setting.

#### D7. Manga Page Stretch (None/Stretch) — S
Web: `MANGA_PAGE_STRETCH_OPTIONS`, only enabled for LONG_STRIP + Larger/Contain fits. Tenji: long strip hardcodes the Stretch behavior (always full-width), so only the "None" alternative + toggle are missing — and "None" is only meaningful once D6 exists. Marginal on a phone; real on tablet/landscape.

## Ported — confirmation table

Verified by the area agents' primary pass (not re-run through the adversarial gap-verifier). Recent-delta echoes merged into their area rows.

| Feature | Area | Tenji evidence / note |
|---|---|---|
| Generated API surface sync vs 3.9.5 | types | `src/api/generated/*` — only diff is the DummyDebrid mock (n/a); 283 vs 281 endpoint keys; remaining deltas are scaffolding-comment + CRLF noise |
| PrewarmStatusItem truthful-badge semantics | types | `prewarm-badge.tsx:37` — wire shape unchanged; stale generated doc-comment is inert |
| iOS-fullscreen subtitle crop compensation | player | `player.tsx syncIosSubtitleCropCompensation` — native reimpl of web's quirk handling |
| Control Center / lock-screen now-playing | player | `player.tsx nowPlayingMetadata` — native equivalent of web MediaSession |
| Truthful per-step loading status text (+ human-readable labels, `b0db2ada`) | player | `session.ts TorrentStreamLoadingState` / `loadingLabel` memo — matches web granularity |
| Live stream stats + stop while loading | player | `anime-entry-torrent-stream-section.tsx:142-198` (Cancel on loading banner — web's loading branch lacks one; progress/seeders/speed + Stop on active banner), `session.ts` WS handler, `use-torrent-stream-controller.ts:834`. Originally misfiled as a gap. Deltas: no upload speed; entry-screen placement vs in-player overlay |
| Relations / Recommendations / Characters (anime entry) | entry | `anime-entry-info-view.tsx` — stale "deferred" baseline corrected; Tenji adds Characters, which web's entry page lacks; behind an Info tab (cosmetic) |
| Up next / durable last-watched sort | library | `src/lib/utils/filtering.ts` — near line-for-line port |
| Continue-watching rows, theme-driven sort | library | `continue-watching.tsx`, `theme-settings.ts` — neither client exposes a page-level sort control |
| Collection sort/filter engine | library | `src/lib/utils/filtering.ts` — engine ported; gaps M10-M12 are UI wiring on top |
| My-Lists AniList tags filter | library | `my-lists.tsx` + `collection-filter-sheet.tsx` — closes a prior false-gap |
| Discover: Aired Recently (14d) | discover | `discover-queries.ts useDiscoverRecentReleases` — identical window/filters; web unchanged since the v3.9 merge |
| Discover: You Might Have Missed | discover | `useDiscoverMissedSequels` — same hook name, same section title |
| Discover: Trending + rotating hero + genre filter | discover | native hero carousel + `MediaGenreSelector` — same 18-genre list; web's trailer-autoplay hero reimplemented natively |
| Discover: Trending manga JP/KR/CN | discover | `useDiscoverTrendingManga` + per-country sections |
| Schedule: Missing Episodes row | schedule | `schedule/index.tsx MissingEpisodesRow` (adult-filtered, spoiler-aware) |
| Schedule: Upcoming Episodes row | schedule | `UpcomingEpisodesRow` + `useGetUpcomingEpisodes` |
| Schedule: release calendar + status filter + watched indication | schedule | week-view substitute for web's month grid; same substantive filters (+Repeating, extra); web's image-transition/week-start toggles not meaningful natively |
| Advanced search: title/type/sort/format/country/season/year | search | `search-constants.ts` + `search-atoms.ts` — 1:1 param match incl. web's SEARCH_MATCH and START_DATE_DESC quirks |
| Advanced search: genre multi-select | search | exact 18-genre match; toggle grid vs combobox is cosmetic |
| Advanced search: tags multi-select | search | `SEARCH_MEDIA_TAGS` ~420 entries, same MediaTagCollection source, same adult gating |
| Advanced search: min score | search | `SEARCH_MIN_SCORES` [9..1], single select as on web — prior "My-Lists min-score gap" was a misattribution |
| Advanced search: status multi-select | search | `AL_MediaStatus[]` on both sides — genuinely multi |
| Advanced search: adult toggle | search | same `enableAdultContent` conditional |
| Advanced search: franchise grouping + pagination | search | `useGroupedById` + auto-load-on-scroll (arguably better than web's click-to-load) |
| Per-user debrid override | settings | `account.tsx` — field-for-field match |
| Per-user password change | settings | `account.tsx` + `useUserChangePassword` |
| Per-user server login/logout | settings | `user-login-screen.tsx`, `user-auth.hooks.ts` |
| External player link/picker | settings | `profile/index.tsx` — ExternalPlayerPickerSheet (applicable subset; engine choice is n/a) |
| Watch-rooms discovery + in-room UI | nakama | `watch-rooms-sheet.tsx`, `room-stream-join-fab.tsx`, `watch-room.ts` reconnect re-join — feature-complete incl. control toggles, force-tracks, autoskip vote; "Leave" vs host "Close room" label is the only cosmetic delta |
| Watch-room sync quality (drift/echo/buffering) | nakama | `use-watch-room-sync.ts` — speed-nudge convergence, echo guard, buffering hold, half-RTT compensation; Tenji ahead of web on drift smoothness |
| Reading Mode (strip/single/double) | manga | `manga-reader-state.ts` + settings sheet |
| Reading Direction LTR/RTL | manga | incl. scaleX flip for RTL paged lists |
| Double Page Offset | manga | StepperRow 0-6, double-page only |
| Page Gap + Gap Shadow | manga | toggles + an adjustable gap slider web lacks (superset) |
| Reading Progress Bar toggle | manga | settings sheet ToggleRow |
| Reset reader settings | manga | footer Reset, disabled at defaults |
| Zoom | manga | `manga-reader-zoom-surface.tsx` native pinch/double-tap — equivalent-or-better mechanism, no zoom-percent readout |
| Double-page narrow-screen handling | manga | landscape orientation lock instead of web's under-950px disable — same problem solved |
| Long Strip reader | manga | full-zoom ScrollView + FlashList fallback + Android warmup tuning (extra perf work web doesn't need) |
| Page scrubber / jump | manga | draggable PageScrubber slider |
| Chapter nav + AniList progress sync + next-chapter prefetch | manga | `doSyncProgress`, `navigateToChapter`, warm prefetch (off for local provider) |
| Provider / scanlator / language filters | manga | `manga-entry-chapters-view.tsx` conditional selects |
| Manual chapter-to-source matching | manga | `manga-manual-match-modal.tsx` — UNVERIFIED beyond presence (not field-diffed) |
| Bulk chapter selection + batch download | manga | long-press selection mode (interaction-pattern substitute for checkboxes) |
| Chapter download queue management | manga | `manga-download-queue.tsx` — more granular than web (per-item retry/delete + Resume/Retry All) |
| Downloaded chapters/media listing | manga | `manga-downloads.tsx` + disk usage + clear-all danger zone. Architectural note: web = server-side chapter cache, Tenji = device-local offline files — deliberate, not a gap |
| Manga entry Relations (anime adaptations) | manga | `manga-entry-info-view.tsx` — corrects the stale deferred-baseline claim for manga |
| Manga entry Recommendations | manga | shared HorizontalMediaCardList |
| Manga entry Characters | manga | up to 20 with role |
| Manga entry description | manga | stripHtml render |
| Continue-reading / download action bar | manga | `manga-entry-action-bar.tsx` (getPreferredStartChapter + badges) |
| Three-tab manga entry nav + offline redirect | manga | `MangaEntryViewSwitcher`, auto-redirect to downloaded tab offline |
| Debrid stream reconnect-resume (WS drop / server restart) | streams | `debrid-reconnect.ts` — same re-issue-once pattern; web's 7dec652f extension was about its dual players, moot here |
| Prewarm/preload badge (orange/red tiers) | streams | `prewarm-badge.tsx` — same colors, shared hook; correct web commit is `c68e31fe` (the `78dd0aeb` cite was an unrelated plugin-tray fix) |
| Prewarm on entry-page mount | streams | `entry/anime/[id]/index.tsx:41-46` prewarms next episode |
| In-playback next-episode prewarm | streams | `player.tsx:787-818` fires at 3s vs web's ~80% — Tenji ahead; earlier "dead hook" suspicion was a scoped-grep error, corrected |

## N/A — not applicable to the iOS client

- **DummyDebrid mock provider** (settings endpoints, `Models_DummyDebrid*` types, feature flag) — internal test scaffolding, correctly excluded from the client's generated layer (merged recent-delta echo).
- **`MpvCore_ClientEventType` "startup-timing" event** — Electron-only MpvCore IPC perf instrumentation; Tenji's expo-mpv-player has its own event model.
- **`Nakama_WatchPartySessionMediaInfo.media` field** — unconsumed backend groundwork on the legacy watch-party model; Tenji's room UI uses `Nakama_RoomCard`. Cheap re-check next sync if web starts consuming it.
- **Settings-secret redaction for non-admins** (`7dec652f`) — server-side; wire shape unchanged; nothing for any client to implement (merged settings-profile echo).
- **Chromecast casting** — Electron IPC (`window.electron.cast`); no AirPlay analog exists in Tenji to port onto.
- **Mini-player float/expand controls** — superseded by Tenji's native iOS Picture-in-Picture.
- **Anime4K / GLSL shaders, debanding** — desktop mpv-prism/libplacebo pipeline; no shader surface in expo-mpv-player.
- **Screenshot-to-directory** — desktop directory-picker flow; a Photos-save equivalent would be a new capability, not a parity port.
- **Playback diagnostics/stats overlay** — blocked on native module fields (only codec/fps exposed today); power-user tooling.
- **MediaSyncTrackButton** — source-verified as Denshi's simple local-offline toggle (raw-notes mislabel corrected); Tenji's own offline-download system supersedes it; porting would be a regression.
- **Plugin webview slots / entry-page plugin buttons** — no plugin host exists in the RN app.
- **"Local Account" settings tab** — its nav trigger is commented out on web itself; vestigial in the source of truth.
- **Denshi playback-engine settings** (VideoCore vs MpvCore, mpv logging, mpv.conf) — no engine choice on iOS.
- **`ui-settings.tsx` theming** (custom CSS, banners, transparency, color banks) — DOM-specific presentation layer.
- **Sort-order defaults + unwatched/unread count-display theme settings** — Tenji consumes these server-pushed values (`theme-settings.ts` + card/collection hooks); deliberately not editable on the phone, consistent with web-owned theming.
- **PlayerSyncControl dual-backend sync bridge** (`7dec652f`) — bridges web's two player backends (VideoCore DOM vs MpvCore IPC); Tenji has exactly one native player passed directly into `useWatchRoomSync` (merged 3 echoes). Every behavior the bridge preserves was separately verified present in Tenji.
- **Non-participant guard on host-stop** (`e0edcaf3`) — backend Go fix on the legacy peer/host watch-party system Tenji never implemented; protects all clients automatically.
- **Manga Page Container Width** — depends on the missing Page Fit=Larger mode (D6); not independently applicable.
- **Reader keybinds / keyboard-shortcut list** — no hardware keyboard; web gates it behind `!isMobile` itself.
- **Discord Rich Presence while reading manga** — no Discord RPC API on iOS.
- **Plugin webview slots on the manga entry page** — no plugin system in Tenji.
- **Floating play-pill z-order fix** (`7dec652f`) — Tenji has no pill/full-screen-loading duality; the bug cannot occur.
- **Loading-status source-of-truth race fix** (`a5ddaf67`) — Tenji bifurcates the loading label per streamMode at the source; the race is structurally impossible.
- **Hover-triggered prewarm on continue-watching cards** — mouse-only interaction (`onMouseEnter`); no touch equivalent needed.
- **Plugin tray "new" badge persistence** (`78dd0aeb`) — no plugin tray UI exists in Tenji to regress.

## Appendix — dropped / reclassified claims (do not rediscover)

- **Nothing was dropped as "not real in web" this pass** — every verified gap's web ref checked out (dropped count: 0).
- **Reclassified by the adversarial verifier:**
  - "Live torrent/debrid stream stats overlay while loading" — claimed missing, actually **PORTED** (the area agent's grep missed `session.ts` + `anime-entry-torrent-stream-section.tsx` + `stopCurrentStream`). See ported table.
  - "Full torrent search → download-to-library" — claimed wholly absent, actually **PARTIAL** (`server-download-modal.tsx` reimplements it; the real gaps are the `isLocalServer` trigger gate and selection depth). See P4.
  - "Onlinestream HLS audio-track preference" — claimed wholly missing, actually **PARTIAL** (global `preferredAudioLanguages` layer in `player-preferences.ts` covers auto-selection; per-media persistence/auto-capture is what's missing). See P3.
- **Corrections that would otherwise resurface:** MediaSyncTrackButton is a local-offline toggle, not the AniList sync trigger (the real sync page is web's `/sync`, outside entry scope); the baseline "deferred: Relations/Recommendations" is stale — ported on both anime (`anime-entry-info-view.tsx`) and manga entries; the earlier "My-Lists min-score gap" was a misattribution (feature only exists under `search/`, where it IS ported); the prewarm-badge web commit is `c68e31fe`, not `78dd0aeb`; the magnet command lives at `_features/sea-command/`, not `_components/`.
- **UNVERIFIED leftovers flagged above:** Tenji `joinStream`'s omitted `directCdnCapable` field (M1 note, for the CDN-handoff owner); manga manual chapter-matching modal field-level diff; `Nakama_WatchPartySessionMediaInfo.media` consumption re-check next sync.
