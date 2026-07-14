# Parity audit — Manga (entry + reader + downloads)

Web ref = `seanime-web` (Denshi UI) at current HEAD. Tenji ref = `seanime-tenji` HEAD (post v0.1.24, 65f8baf).

`git log --since=2026-07-04 --oneline -- 'seanime-web/src/app/(main)/manga' 'seanime-web/src/app/(main)/(library)/offline'` → **empty**. No web manga changes since the 2026-07-04 baseline; this audit compares against the same surface Tenji v0.1.24 was already targeting.

## Reader settings (web: `chapter-reader-settings.tsx`, 677 lines; tenji: `manga-reader-state.ts` + `manga-reader-settings-sheet.tsx`)

- Reading Mode (Long Strip / Single Page / Double Page) — ported, `MANGA_READING_MODE` identical enum in both.
- Reading Direction (LTR/RTL) — ported.
- Double Page Offset — ported (web: NumberInput; tenji: Stepper 0-6), shown only in double-page mode in both.
- Page Gap toggle + amount — ported, tenji ADDS an adjustable gap-amount slider (0-24px) where web's toggle is binary-only (web has a separate "Page Gap Shadow" switch too, also ported).
- Progress Bar toggle — ported.
- Reset-to-defaults — ported (both disable the button when settings already equal defaults).
- **Page Fit** (Contain/Overflow/Cover/True size) — CONFIRMED MISSING. `MangaReaderSettings` type in `manga-reader-state.ts` has no `pageFit` field; `manga-reader-settings-sheet.tsx` has no corresponding UI section. Matches baseline's deferred-known bucket.
- **Page Stretch** (None/Stretch) — CONFIRMED MISSING, same evidence as above (no `pageStretch` field, no UI).
- **Zoom** (web: NumberInput + Reset, percentage-based) — NOT missing, but implemented via a different, more platform-native mechanism: `manga-reader-zoom-surface.tsx` (827 lines) gives native pinch-to-zoom + double-tap-to-zoom on both reading axes (iOS: native `ScrollView` `pinchGestureEnabled`/`maximumZoomScale`; Android: custom Reanimated pinch/pan/double-tap worklets). No settings-panel zoom% control exists, by design — classified as ported via substitution, not a gap.
- Page Container Width (only relevant when web pageFit===LARGER) — n/a, dependent on the missing Page Fit feature.
- Editable Keybinds section (web, desktop only `!isMobile`) — n/a, no hardware keyboard on iOS by default; static "Keyboard Shortcuts" list also n/a for the same reason.
- Web's `<950px` double-page auto-disable (desktop breakpoint) — tenji instead auto-locks screen orientation to landscape for double-page mode (`ScreenOrientation.lockAsync`, plus an iOS accelerometer-driven landscape-left/right flip) — a mobile-appropriate substitute solving the same "double page needs width" problem, not a gap.

## Reader implementation (`manga-reader-screen.tsx`, 1418 lines)

- Long Strip: full-zoom continuous ScrollView path (`useFullLongStripZoom`) plus a virtualized FlashList fallback path for pages with missing/very-tall aspect ratios — a performance feature web doesn't need (it's not managing FlashList virtualization/recycling). Android gets an additional `useMangaReaderAndroidLongStrip` warmup/draw-distance tuning hook not present for iOS or web — device-specific tuning, not a parity gap.
- Paged/Double-page: horizontal FlashList with `pagingEnabled`, RTL support via `scaleX: -1` transform on both the list and image content (mirrors web's RTL chapter-nav-button swap logic in `manga-reader-bar.tsx`).
- Page scrubber (bottom-center) — a draggable Slider-based page-jump control tied to spread index; ported equivalent of web's page pagination badge+popover+select in `manga-reader-bar.tsx`.
- Chapter prev/next nav, auto progress-sync to AniList on last-page reached, "keep next chapter warm" prefetch (disabled for `local-manga` provider to avoid cache pollution) — all ported.
- Offline-chapter-unavailable state card, load-error state card — ported (web doesn't need these since it's always server-connected).

## Provider / filtering / manual match (tenji: `manga-entry-chapters-view.tsx`, `manga-manual-match-modal.tsx`; web: `chapter-list/*`)

- Source/provider select, scanlator filter, language filter (conditionally shown only if options exist) — ported.
- "Unread only" filter — ported.
- Manual chapter-to-source matching modal — ported: `seanime-tenji/src/components/features/manga/manga-manual-match-modal.tsx` vs web's `seanime-web/src/app/(main)/manga/_containers/chapter-list/manga-manual-mapping-modal.tsx` (210 lines). Confirmed both exist; did not diff field-by-field but presence + purpose match.
- Empty-cache/refresh action — ported (`useEmptyMangaEntryCache`).
- Smart default landing page (lands on first-unread chapter's page) — tenji-specific UX addition, not present as such on web (web just lists all chapters, paginated).
- Bulk selection (long-press → multi-select → batch download) — ported. Web: `chapter-list-bulk-actions.tsx` (Download selected chapters (N) button). Tenji: long-press activates `selectionMode` in `manga-entry-chapters-view.tsx`/`manga-entry-screen.tsx`, wired to `MangaEntryActionBar`'s download button. Same capability, native long-press gesture instead of row checkboxes.

## Downloads UI (tenji: `manga-downloads.tsx`, `manga-download-queue.tsx`; web: `chapter-downloads-drawer.tsx`)

- Web `ChapterDownloadQueue`: Start/Stop/Reset-errored/Clear-all (global buttons), per-item indeterminate progress, errored badge. `ChapterDownloadList`: downloaded-media grid w/ chapter-count badges, separate "not in AniList collection" section.
- Tenji `manga-download-queue.tsx`: separate dedicated queue screen — Overview stats, "In Queue" (Resume-All for stalled + Clear Queue + per-item progress/retry/delete, paginated 30/page), "Failed" (Retry-All + per-item retry/delete, paginated). Finer-grained (per-item vs web's global-only controls) — ported, arguably exceeds web.
- Tenji `manga-downloads.tsx`: disk-usage stat surface (formatted bytes, downloaded-chapters count, downloaded-manga count) — mobile-native addition, no clear web equivalent (web doesn't surface local disk usage since chapters download server-side, not to the browser). Paginated downloaded-manga list (24/page), "Clear all manga downloads" danger-zone action with confirming disk-space-to-free alert — ported/exceeds web's simpler grid.
- **Important architectural note**: web's manga chapter downloads live on the SERVER (chapters cached server-side so the reader doesn't re-hit the provider); tenji's downloads are DEVICE-LOCAL files for true offline reading (`use-manga-downloads.ts`, `manga-download-store.ts`). This is a deliberate, sensible platform difference for a mobile client with no always-on companion server — not a gap, but worth noting as "same feature name, different architecture."

## Entry screen (tenji: `manga-entry-screen.tsx`, `manga-entry-info-view.tsx`, `manga-entry-action-bar.tsx`; web: `manga/entry/page.tsx`, `meta-section.tsx`, `manga-recommendations.tsx`)

- Three-tab view switcher (chapters / info / downloaded) with lazy-mounted views, auto-redirect to "downloaded" when offline — tenji-specific navigation pattern (mobile tab affordance replacing web's single continuous-scroll page); functionally covers the same content web puts on one page.
- Description — ported (HTML-stripped in tenji's info view; web renders raw via a rich-text-ish block in `meta-section.tsx`).
- Characters (up to 20) — ported.
- **Anime Adaptations (relations)** — ported. `manga-entry-info-view.tsx` filters `relations.edges` to `type === "ANIME"` + format TV/MOVIE/TV_SHORT, renders horizontal `MediaEntryCard` list with relation-type label overlay. **This contradicts the general parity baseline's "deferred-known: entry Relations & Recommendations rows" for the manga area specifically** — that deferred bucket appears to describe the anime-entry screen, not manga. Reporting as ported here, correcting the baseline for this area.
- **Recommendations** — ported. Rendered via shared `HorizontalMediaCardList` component (`type="manga"`, `showAudienceScore`), same data shape as web's dedicated `manga-recommendations.tsx`.
- Continue-reading / Download action bar (`manga-entry-action-bar.tsx`) — ported: "Start Reading"/"Ch. N" continue button (`getPreferredStartChapter`) + Download button with downloaded-count/queue-length badge.
- Discord Rich Presence for manga reading (`handle-discord-manga-presence.ts`, web-only) — n/a, no Discord RPC API on iOS.
- Two `PluginWebviewSlot`s on web's entry page (plugin extension points) — n/a, tenji has no plugin/extension-webview system.

## Coverage / method

Read in full: `chapter-reader-settings.tsx` (677L), `manga-reader-bar.tsx` (342L), `chapter-vertical-reader.tsx` (258L), `manga/entry/page.tsx` (105L), `chapter-downloads-drawer.tsx` (293L), `chapter-list-bulk-actions.tsx` (39L) on the web side; `manga-reader-state.ts` (145L), `manga-reader-settings-sheet.tsx` (335L), `manga-reader-zoom-surface.tsx` (827L), `manga-reader-screen.tsx` (1418L), `manga-entry-screen.tsx` (287L), `manga-entry-action-bar.tsx` (106L), `manga-entry-chapters-view.tsx` (434L), `manga-entry-info-view.tsx` (196L), `manga-downloads.tsx` (214L), `manga-download-queue.tsx` (296L) on the tenji side. Confirmed via `find`/`ls` presence (not full diff) of `manga-manual-match-modal.tsx` matching web's `manga-manual-mapping-modal.tsx`. Not read in full (lower priority, no evidence of a gap from names/greps): `manga-entry-downloaded-view.tsx`, `manga-entry-view-switcher.tsx`, `manga-pagination-controls.tsx`, `manga-reader-utils.ts`, `manga-reader-layout.ts`, `use-manga-reader-android-long-strip.ts`, `meta-section.tsx` (web).
