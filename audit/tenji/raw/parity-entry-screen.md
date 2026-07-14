# Parity audit — Anime entry screen (Tenji vs seanime-web/Denshi)

Area: metadata header, episode lists, torrent search/select, relations/recommendations,
artwork prefetch hook (2397ef32). Compared at seanime HEAD (5a8a9b74, 3.9.5) vs
seanime-tenji HEAD (65f8baf, 0.1.24, 2026-07-09).

## Structure comparison

- web: `seanime-web/src/app/(main)/entry/_containers/anime-entry-page.tsx` — tabbed views:
  library / torrentstream / debridstream / onlinestream / plugin episode tabs. Meta section +
  season switcher always on top; bottomSection (characters + relations/recommendations) is
  appended under whichever view is active (episode list, torrent-stream, debrid-stream,
  online-stream all get it).
- tenji: `app/(app)/entry/anime/[id]/index.tsx` -> `src/components/features/media/anime-entry-screen.tsx`
  — bottom-tab-bar views: library / server-local / torrentstream ("Stream", merges
  torrent-client + debrid via an in-sheet StreamMode toggle) / onlinestream / info / downloaded.
  Relations/Recommendations/Characters live under a dedicated **Info** tab
  (`anime-entry-info-view.tsx`), not inline under every other view.

## Meta / header

- web `_components/meta-section.tsx`: cover, banner, title (userPreferred/english/romaji),
  start date, season, status, audience score, studio (`AnimeEntryStudio`), genre list,
  `AnimeEntryRankings` (AniList "#N most popular/highest rated" badges), trailer button,
  AniList/AniDB/MAL external links, `NextAiringEpisode` countdown ("Episode N in X · Weekday"),
  dropdown menu (open directory / library explorer / AniDB / MAL / metadata manager / bulk
  download-unmatch-delete), `AnimeAutoDownloaderButton` (opens rule editor for entry),
  `MediaSyncTrackButton`,

  **Correction**: `MediaSyncTrackButton` is NOT the AniList upload/sync trigger from the
  deferred-known list — verified its source
  (`_features/media/_containers/media-sync-track-button.tsx`): it calls
  `useLocalAddTrackedMedia`/`useLocalGetIsMediaTracked`/`useLocalRemoveTrackedMedia` and its
  tooltip literally reads "Save locally" / "Remove offline data" — it is Denshi's own
  local-tracked-media offline-download toggle, unrelated to AniList. The actual AniList
  upload/sync trigger lives at `seanime-web/src/app/(main)/sync/page.tsx`, a standalone
  settings-area page outside this audit's scope (entry page). Classifying
  `MediaSyncTrackButton` as n-a here: tenji already has a more capable native offline system
  (`src/lib/offline/*`, `server-local-sync-service.ts`, batched `Image.prefetch`), so porting
  this simpler toggle would be a regression, not a gap.
  `AnimeEntrySilenceToggle` (mute list-sync notifications for entry), `ToggleLockFilesButton`.
- tenji `media-entry-header.tsx` (`MediaEntryHeaderContent`) + `anime-entry-action-bar.tsx`:
  cover, title, alt title, start date/season/status, audience score, genres (top 3),
  progress + list-status pill, `EditAnilistEntry`, Continue-watching / Download / Server-download
  buttons. **No** studio name, **no** rankings badges, **no** next-airing-episode countdown text,
  **no** trailer button, **no** AniDB/MAL external links, **no** silence toggle, **no**
  auto-downloader-rule button, **no** sync-track/upload button, **no** file-management dropdown
  (open directory/explorer/metadata manager/unmatch/bulk-delete — these are server-filesystem
  management concepts with no analogue in a thin iOS client that doesn't browse the server disk).
  Confirmed via grep: `nextAiringEpisode` never rendered as UI text anywhere in tenji (only used
  internally for episode-progress math); no `Silence`/`SyncTrack`/`studios`/`Rankings` hits in
  `src/components/features/media` or `src/components/features/anime`.

## Relations / Recommendations / Characters — FALSE GAP CHECK

The task's known-deferred list says "entry Relations & Recommendations rows" is still missing.
**This is stale / a false gap as of this audit.** `anime-entry-info-view.tsx` in tenji already
renders Description, Characters (up to 20, with role labels), Relations (horizontal card list,
including SOURCE/ADAPTATION source-manga card with "Manga" overlay badge, relation-type label +
"(Movie)" suffix — same logic as web's `relations-recommendations-section.tsx`), and
Recommendations (`HorizontalMediaCardList`). Verified via `git log` that this file has existed
since commit 84ccb15 ("changelog"), well before the 0.1.24 parity batch — this was not a recent
addition, the deferred-known list in the task brief is simply out of date for this item.
Functional parity is solid; the one real UX difference is **placement**: web shows
Relations/Recommendations/Characters inline under every view (episode list, torrent-stream,
debrid-stream, online-stream), always visible without navigating away. Tenji gates all three
behind a separate "Info" tab the user must explicitly switch to. Classifying as **ported**
(feature present, comparable quality) with a note about the discoverability difference, not as
missing.

## Artwork prefetch hook (2397ef32, shipped 2026-07-10 — TODAY, same day as this audit)

- web: `useAnizipArtworkPrefetch(mediaId)` in `video-core-loading-screen.tsx`, called from
  `anime-entry-page.tsx` line 126. Fetches server-cached ani.zip artwork (`fanart`/`logo` URLs,
  7-day filecache via new `/api/v1/anizip-artwork/:id` route) and warms the browser image cache
  (`new Image(); img.src = url`) as soon as the entry page mounts — before the user presses play.
  The actual `VideoCoreLoadingScreen` component then gates all visuals (backdrop + clearlogo)
  until both images report `onLoad`, avoiding a jarring text-then-image pop-in.
- tenji: **no equivalent.** Grepped `anizip`/`Anizip`/`fanart`/`clearlogo`/`backdrop` across
  `src`/`app` — the only ani.zip usage is `fetchAniZipMapping` in
  `src/components/features/player/player-panel.tsx`, which is episode/subtitle metadata mapping,
  unrelated to artwork. No `Image.prefetch` call anywhere in `anime-entry-screen.tsx` or the
  entry route. No loading-screen component in `src/components/features/player/` shows fanart
  artwork or a title logo while mpv is starting/buffering (grepped
  `loading|Loading|isBuffering|buffering|starting` in player-panel.tsx / player-overlays.tsx —
  zero hits related to a full-screen artwork loading state). Tenji has its own native mpv player
  (expo-mpv-player) and SeaImage (expo-image, disk-cached) is already used elsewhere for
  banner/cover art, so both the server endpoint (just call it) and the client caching primitive
  (`Image.prefetch` from expo-image) exist — this is a straightforward, applicable, and brand-new
  gap, not something the 0.1.24 batch could have covered (it postdates that release by ~15 hours).

## Episode lists

- web: `_containers/episode-list/episode-section.tsx`, `episode-item.tsx`,
  `undownloaded-episode-list.tsx`, `_components/episode-list-grid.tsx`,
  `_components/merged-season-section.tsx`.
- tenji: `AnimeEntryLibraryView` (SectionList: Episodes/Specials/NC sections, per-section
  pagination via `EpisodePageSelector`, first-unwatched-page auto-jump, thumbnail width
  responsive to screen width, watch-history percentage bars via `getEpisodePercentageComplete`),
  `EpisodeCardList` for "Continue Watching" horizontal rail, `EpisodeListItem`. Season
  grouping/merged-season already confirmed ported per baseline (media/merged-season-section.tsx,
  media/season-switcher.tsx exist and are wired into `anime-entry-screen-context` type flows —
  not re-verified deeply here per instructions to keep already-ported items cheap-check only).
  This area is strong parity, arguably more mobile-appropriate (paged sections vs web's
  scroll-all-episodes grid).

## Torrent search / select (deep-dived — `torrent-stream-picker-sheet.tsx`,
`use-torrent-stream-controller.ts`)

Full parity confirmed, high quality:
- Provider selection, smart search vs simple query, resolution filter, best-release toggle,
  batch-search toggle, search-across-additional-providers (multi-provider stage with select
  all/clear all), previous-batch-history reuse with "auto-selected on episode tap" note,
  file-selection stage with "Likely" badge, download-vs-stream mode segmented control,
  destination-path field for downloads, download Missing/Full split for batches.
- Debrid instant-availability Cached/Uncached filter chips — already confirmed ported per
  baseline, re-verified present (`isCached`/`cachedFilter`/`showCacheFilter` in
  `TorrentSelectionStage`).
- Torrent card: release-group, resolution badge (color-coded), seeder count with battery icon
  tiers, formatted size, relative date, provider name, magnet/link copy-to-clipboard, confirmed
  badge, best-release "Highest quality" badge, dub/multi-sub/dual-audio metadata tags parsed from
  Habari metadata — this matches (and in the magnet-copy / metadata-tag department, arguably
  exceeds) web's `torrent-preview-item.tsx`/`torrent-item-badges.tsx`/`torrent-table.tsx`.
- Magnet-to-library paste UI (9f6e250c on web — paste a raw magnet link to add directly to
  library without searching): grepped tenji for "magnet" paste/input UI — only found in the
  torrent-card copy-to-clipboard direction (copying magnet OUT, not pasting one IN). Confirmed
  still missing, per the task brief's known-deferred list. Reporting as **deferred-known**.

## Debrid streaming — structural note

Web keeps debrid streaming as a fully separate top-level view/tab (`debridstream`) alongside
`torrentstream`, each with independent pages (`debrid-stream-page.tsx` vs
`torrent-stream-page.tsx`). Tenji merges both into a single "Stream" tab
(`AnimeEntryTorrentStreamSection` + `torrent-stream-picker-sheet.tsx`'s `streamMode` segmented
control: "Torrent Client" vs "Debrid Service"). This is a reasonable UX consolidation for a
smaller screen, not treated as a gap — functionality (per-user debrid settings, instant
availability, file preview, prewarm) is present per baseline and spot-checks above.

## Known-deferred items re-verified still absent (not new discoveries)

- Magnet-to-library paste UI (9f6e250c) — confirmed absent, see above.
- Auto-downloader rules manager — confirmed absent; web's per-entry
  `AnimeAutoDownloaderButton` opens `AutoDownloaderRuleForm` (create/edit rule scoped to this
  anime); tenji has no equivalent button/screen in the entry action bar or dropdown.
- AniList upload/sync trigger — confirmed absent (`MediaSyncTrackButton` on web, no analogue
  found anywhere in tenji's media/anime feature folders).
- Entry Relations & Recommendations rows — **RECLASSIFIED, see section above: this is actually
  ported**, not missing. The task brief's deferred list is stale for this specific item.

## Grep evidence for "missing" claims

```
grep -rn "nextAiringEpisode" src app   # only in edit-anilist-entry.tsx, media-entry-card.tsx,
                                        # filtering.ts — math only, never rendered as text/UI
grep -rln "AnimeAutoDownloader|MediaSyncTrackButton|SilenceToggle|ToggleLockFiles|
            MetadataManager|UnmatchFiles|BulkDelete" src app   # zero hits
grep -rln "studios|Studio" src/components/features/media src/components/features/anime  # zero
grep -rln "rankings|Rankings" src   # zero
grep -rln "anizip|Anizip|AniZip|fanart|clearlogo|backdrop" src app
  -> only player-panel.tsx fetchAniZipMapping (episode mapping, not artwork)
grep -n "loading|Loading|isBuffering|buffering|starting" player-panel.tsx player-overlays.tsx
  -> no hits (no full-screen artwork loading gate)
```
