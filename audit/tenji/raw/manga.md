# Tenji manga sweep — raw audit notes

Scope: `src/components/features/manga/**` (18 files) + manga-specific `src/lib/downloads/*` (manga-download-manager.ts, manga-download-store.ts, use-manga-downloads.ts, index.ts barrel) + a scoping skim of download-queue-resume-service.ts and native-download.ts (shared anime/manga infra, only manga-relevant paths reviewed).

## Files read (full)

Components:
- reader/manga-reader-state.ts
- reader/manga-reader-utils.ts
- reader/manga-reader-layout.ts
- reader/manga-reader-screen.tsx (1417 lines)
- reader/manga-reader-zoom-surface.tsx (827 lines)
- reader/use-manga-reader-android-long-strip.ts
- reader/manga-reader-settings-sheet.tsx
- manga-entry-chapters-view.tsx
- manga-entry-screen.tsx
- manga-entry-downloaded-view.tsx
- download-chapters-modal.tsx
- manga-entry-action-bar.tsx
- manga-entry-info-view.tsx
- manga-entry-view-switcher.tsx
- manga-manual-match-modal.tsx
- manga-pagination-controls.tsx
- manga-entry-screen-context.tsx
- downloaded-manga-list.tsx

lib:
- lib/downloads/manga-download-manager.ts (897 lines, full read)
- lib/downloads/manga-download-store.ts (452 lines, full read)
- lib/downloads/use-manga-downloads.ts (468 lines, full read)
- lib/downloads/index.ts (barrel, full read)
- lib/downloads/native-download.ts (shared generic download primitive used by manga chapter page downloads — reviewed for manga call-site correctness)
- lib/downloads/download-queue-resume-service.ts (shared anime+manga resume service — reviewed manga branches only)

Reference only (not audited, used to avoid re-reporting / to resolve scope questions):
- tenji-audit.md ledger (T1-T13 fixed, none touch manga reader/download internals)
- src/hooks/use-manga-chapters.ts (peeked only at `useHandleMangaChapters`'s provider filtering, to resolve whether `chapters` passed into `getPreferredStartChapter` spans multiple providers — it does not, confirmed single-provider-filtered)

Not read (deliberately out of scope, confirmed generic/anime-only, not manga-specific): download-manager.ts, download-store.ts, use-downloads.ts, offline-entry-resolver.ts, offline-entry-store.ts, mutation-queue.ts, query-persistence.ts, and other non-manga src/lib files.

## Findings kept (see StructuredOutput for authoritative list)

1. **[bug] Per-manga "Downloaded" tab shows global disk usage, not this manga's usage.**
   `manga-entry-downloaded-view.tsx:53` calls `getMangaDownloadDiskUsage()` from `manga-download-manager.ts:889-893`, which computes `getMangaDownloadsDir().size` — the size of the ENTIRE `manga-downloads` root directory (all media, all providers), not `getMediaDir(mediaId)`. The rendered line at `manga-entry-downloaded-view.tsx:68` is `"{completedChapters.length} chapters · {formatBytes(diskBytes)}"` — the chapter count is correctly scoped to `mediaId`, but the byte count is the whole library's footprint. Any user with 2+ manga downloaded sees a wildly wrong number on every manga's Downloaded tab (all of them show the same total).
   Concrete scenario: download 5 chapters of Manga A (50MB) and 5 chapters of Manga B (50MB). Manga A's Downloaded tab shows "5 chapters · 100MB" (should be 50MB); so does Manga B's.

2. **[perf] Same call site does a synchronous recursive filesystem walk on every download-store revision bump, on the JS thread.**
   `Directory.size` (expo-file-system "next" API, confirmed via `node_modules/expo-file-system/build/ExpoFileSystem.types.d.ts:128-130`) is a synchronous native getter that recursively computes total directory size — a blocking JSI call. `manga-entry-downloaded-view.tsx:53`'s `useMemo(() => getMangaDownloadDiskUsage(), [allChapters])` recomputes every time `allChapters` (`useAllDownloadedMangaChapters(mediaId)`) gets a new array reference. `useAllDownloadedMangaChapters` re-derives whenever `useMangaDownloadRevision()` (a `useSyncExternalStore` over an MMKV `addOnValueChangedListener`) fires. During an active chapter download, `reportMangaChapterProgress` (manga-download-manager.ts:231-256) persists progress (and thus bumps the revision) roughly every 250ms or every 2 pages, whichever comes first (`MANGA_PROGRESS_MIN_INTERVAL_MS=250`, `MANGA_PROGRESS_MIN_PAGE_DELTA=2`). So while the Downloaded tab of ANY manga is open and ANY chapter of ANY manga is actively downloading, the JS thread does a full recursive stat-walk of the whole downloads directory (which only grows as the user's library grows) roughly 4x/second.
   Note: a correctly-throttled equivalent hook already exists and is exported — `useMangaDownloadDiskUsage()` in `use-manga-downloads.ts:201-210` / re-exported from `lib/downloads/index.ts:109` — memoized on the same revision, so it has the exact same recompute cadence, but at least computes the intended semantics via the same (buggy) global function. The real fix needs a per-media size function, not just switching call sites.

3. **[smell/low] `getDefaultMangaReaderSettings(isCompact)` — `isCompact` is plumbed end-to-end but has zero effect.**
   `manga-reader-screen.tsx:84,97` computes `isCompact = screenWidth < 900` and passes it into `useMangaReaderSettings(mediaId, isCompact)`, which passes it to `getDefaultMangaReaderSettings(isCompact)` (`manga-reader-state.ts:49-71`). Both the `if (isCompact)` and the fallthrough branch return byte-for-byte identical `MangaReaderSettings` objects (LONG_STRIP/RTL/pageGap on/10/shadow on/progress bar on/offset 0). Reads like a tablet-vs-phone default split (e.g. defaulting tablets to double-page) was planned and never implemented — half-wired, not a parity gap vs web. Capped at low/smell since there's no traceable wrong-behavior, just dead branching.

## Near-misses investigated and rejected (not reported)

- **`handleLongStripViewableItemsChanged` double-negation** (`manga-reader-screen.tsx:548-562`): confusingly written (`if (!firstVisible?.index) { if (firstVisible?.index !== 0) return }`) but traced through all three cases (undefined, index 0, index > 0) — behaves correctly. Style nit only, not reported.

- **`getPreferredStartChapter` missing `preferredProvider` arg to `dedupeByChapterNumber`** (`manga-reader-utils.ts:188`, `dedupeByChapterNumber` accepts an optional `preferredProvider` used by `getChapterCandidateScore` for a +4 tie-break bonus, but the "Start Reading" call site never supplies it). Investigated whether this is user-visible: confirmed via `src/hooks/use-manga-chapters.ts` (`useHandleMangaChapters`) that the `onlineChapters` fed in are already filtered to the single `selectedProvider` — so the only way this omission changes the outcome is when a chapter number is both available online (selected provider) AND downloaded from a *different* provider; in that case the candidate scoring falls back to "downloaded wins" (+2 vs +0) regardless of provider, which is a defensible/arguably-desirable default (prefer the already-downloaded copy for offline reading) rather than a clear wrong-behavior. No concrete failure scenario where the result is objectively wrong (as opposed to "not what a provider-preference reading might expect") — capped out, not reported per evidence-discipline rule (can't trace inputs→wrong-output, only inputs→debatable-output).

- **`getAdjacentChapters`/`pickClosestChapter`**: DOES correctly pass `currentChapter.provider` as `preferredProvider` (`manga-reader-utils.ts:256-257`) — confirms the omission above is isolated to the start-chapter call site, not a systemic pattern bug.

- **Android-only dead code paths**: `use-manga-reader-android-long-strip.ts`'s aspect-ratio premeasure effects and `manga-reader-zoom-surface.tsx`'s `isCustomPinch` (Reanimated worklet pinch/pan) branch are both gated on `Platform.OS === "android"`, unreachable in the shipped iOS-only app. Not reported — this is unreachable code, not broken code; nothing to trace a failure scenario against on the shipped target.

- **`getTotalMangaDownloadSize()`** (`manga-download-store.ts:449-451`) always returns 0 — checked all call sites, it is not imported/called anywhere (not even re-exported from `index.ts`). Dead stub, no user-facing effect. Not reported.

- **Native-download cancellation race** (`manga-download-manager.ts` `downloadPage`/`runPageDownload` in `downloadChapterPages`, `native-download.ts` `toPendingNativeDownload`): `activeNativeDownloadIds.add(downloadId)` only happens *after* `await getManagedNativeDownload(downloadId)` resolves. If `tracker.cancelled` flips true during that await window, `cancelMangaChapterDownload`/`cancelAllMangaDownloads`/app-backgrounding won't find this `downloadId` in the Set yet and won't call `cancelManagedNativeDownload` for it — the single page download can run to completion despite cancellation. Impact is self-healing: the enclosing chapter's directory gets deleted wholesale on cancel-catch (`processQueueItem`, multiple `chapterDir.delete()` sites) or the stray file just sits harmlessly under an already-abandoned chapter dir. No observable user-facing bug (no zombie chapter marked complete, no crash) — narrow timing window, low value even as "low" severity. Not reported.

- **Per-row hook cost in `ChapterListItem`** (`manga-entry-chapters-view.tsx`, up to 30 rows/page each calling `useIsMangaChapterDownloaded`/`useMangaChapterDownloadInfo`): verified against `use-manga-downloads.ts` — every hook is `useSyncExternalStore(revision) + useMemo` over a cheap single-key MMKV read/JSON.parse. Revision only bumps on actual download-store writes, not on unrelated re-renders. Not a perf issue.

- **`batchMangaDownloadStoreWrites` reentrancy** (`retryFailedMangaDownloads` → loop calling `retryMangaChapterDownload`, each opening its own nested `batchMangaDownloadStoreWrites`): verified `revisionBatchDepth` is a counter (increment/decrement), not a boolean flag, so nesting is safe and only flushes the revision once at depth 0. Not a bug.

## Coverage statement

All 18 files under `src/components/features/manga/**` read in full. All manga-specific files under `src/lib/downloads/` read in full (manga-download-manager.ts, manga-download-store.ts, use-manga-downloads.ts, index.ts barrel). Shared anime+manga infra (`native-download.ts`, `download-queue-resume-service.ts`) read in full but only manga call sites evaluated for findings (their anime-only logic is another agent's scope). Nothing in the assigned scope was skipped. One out-of-literal-scope peek was made into `src/hooks/use-manga-chapters.ts` purely to resolve whether a `manga-reader-utils.ts` finding candidate was user-visible (it wasn't) — no findings sourced from that file itself.
