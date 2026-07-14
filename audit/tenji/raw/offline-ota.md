# Tenji audit — scope: src/lib/downloads/, src/lib/offline/, src/lib/ota/

Sweep agent notes. Concerns: download queue lifecycle, resume after app kill,
storage accounting/cleanup, offline-mode data correctness, OTA update flow
safety (apply/rollback/partial download).

## Files read (full read, in scope)

Downloads:
- src/lib/downloads/download-manager.ts (718 lines)
- src/lib/downloads/manga-download-manager.ts (897 lines)
- src/lib/downloads/download-store.ts (508 lines)
- src/lib/downloads/manga-download-store.ts (452 lines)
- src/lib/downloads/native-download.ts (155 lines)
- src/lib/downloads/download-queue-resume-service.ts (75 lines)
- src/lib/downloads/use-downloads.ts (479 lines)
- src/lib/downloads/use-manga-downloads.ts (468 lines)
- src/lib/downloads/index.ts (124 lines, barrel)
- modules/expo-download-manager/ios/ExpoDownloadManagerModule.swift (364 lines, native iOS backing module)
- modules/expo-download-manager/ios/ExpoDownloadManagerAppDelegate.swift (15 lines)

Offline:
- src/lib/offline/manual-offline-mode.ts (15 lines)
- src/lib/offline/mutation-queue.ts (280 lines)
- src/lib/offline/sync-service.ts (61 lines)
- src/lib/offline/use-offline.ts (381 lines)
- src/lib/offline/offline-entry-store.ts (122 lines)
- src/lib/offline/offline-entry-resolver.ts (224 lines)
- src/lib/offline/server-local-store.ts (270 lines)
- src/lib/offline/server-local-sync-service.ts (276 lines)
- src/lib/offline/download-entry-snapshot-store.ts (118 lines)
- src/lib/offline/download-snapshot-refresh-service.ts (135 lines)
- src/lib/offline/index.ts (79 lines, barrel)

OTA:
- src/lib/ota/updates.ts (229 lines) — the entire OTA scope is this single file (uses stock `expo-updates`).

Also read for call-site verification (out of scope, informational only):
- app/(app)/_layout.tsx (BackgroundServices mount site for all offline/download resume hooks)
- app/_layout.tsx (OtaUpdatePrompt mount site, confirmed single root mount)
- H:/Projects/seanime/tenji-audit.md (existing ledger, T1-T13 all fixed — checked to avoid re-reporting)

Coverage: every file under the three scoped directories was read in full. Nothing in scope was sampled/skipped.

## Findings kept (see StructuredOutput for the formal list)

### F1 — Anime "pause on background" is actually a full cancel + restart-from-zero (medium, bug)
`download-manager.ts` `pauseActiveAnimeDownload` (~line 230-246):
```ts
async function pauseActiveAnimeDownload(key: string, tracker: ActiveDownload): Promise<void> {
    ...
    tracker.pausedForAppState = true
    cancelManagedNativeDownload(tracker.downloadId)
    const storedEpisode = getDownloadedEpisode(tracker.mediaId, tracker.episodeId)
    activeDownloads.delete(key)
    markDownloadPending(tracker.mediaId, tracker.episodeId, {
        progress: storedEpisode?.progress ?? tracker.lastProgressValue,
        fileSize: storedEpisode?.fileSize,
    })
    log.info(`Paused download after app backgrounded: ${tracker.episodeId}`)
}
```
Traced `cancelManagedNativeDownload` -> native-download.ts -> Swift `cancelDownloadById`:
```swift
Function("cancelDownloadById") { (id: String) in
  self.session?.getAllTasks { tasks in
    for task in tasks {
      guard self.info(for: task)?.id == id else { continue }
      task.cancel()   // plain cancel, NOT cancel(byProducingResumeData:)
    }
  }
}
```
And `startDownload` (Swift, ~line 104) takes no byte-range/offset/resume-data parameter — every (re)start is
a fresh `URLSessionDownloadTask` from byte 0. So when `backgroundDownloading` is disabled in Settings
(`app/(app)/(tabs)/(profile)/download-settings.tsx`, atom default `true` in `download-settings.atoms.ts:15`)
and the user backgrounds the app mid-download, the record is marked "pending" with the stale progress value
preserved (e.g. 80%) for display, but the underlying transfer is fully cancelled with no resume checkpoint.
On next `resumeAnimeDownload`/foreground, `processQueuedAnimeDownload` calls `startManagedNativeDownload`
again — a brand new download starting at 0 bytes, silently re-downloading everything already fetched.
Concrete scenario: user has "Background downloading" OFF, starts a 1.2 GB episode download, backgrounds
the app at 80% (~960 MB fetched), the UI still shows "pending" at 80%; on foreground the whole 1.2 GB is
re-fetched over cellular/wifi, wasting the ~960 MB already paid for in data. Log message says "Paused" but
behavior is destructive cancel.
Manga is NOT affected in practice: same `task.cancel()` pattern exists (`handleMangaDownloadAppStateChange`),
but a manga "unit" is one page image (tens/hundreds of KB) and already-downloaded pages are skipped via
`destFile.exists` checks in `downloadChapterPages` — so at most one small in-flight page image is
re-fetched, not the whole chapter. Not flagging manga.
Severity: medium (real data/bandwidth waste + misleading "paused" semantics), gated behind a non-default
setting so not critical. Category: bug (mislabeled operation with a real cost), could also be filed as ux.

### F2 — Duplicate `x ?? x` no-op fallback for cover image URL (low, bug, 7 sites / 4 files)
Pattern `entry.media?.coverImage?.large ?? entry.media?.coverImage?.large` (both sides identical — the
right-hand fallback is dead code and does nothing). Confirmed intended pattern by comparing against the ONE
place in scope that got it right, `server-local-sync-service.ts:99`:
```ts
coverImageUrl: media.coverImage?.large ?? media.coverImage?.extraLarge,
```
And confirmed via `AL_BaseAnime_CoverImage`/`AL_BaseManga_CoverImage` generated types that `extraLarge`,
`large`, `medium` are distinct fields — so the intended fallback chain was almost certainly
`large ?? extraLarge` (or similar), not `large ?? large`.
Sites (all in scope):
- `src/lib/downloads/download-manager.ts:148` (`getAnimeLibraryInfo`)
- `src/lib/downloads/manga-download-manager.ts:667` (`enqueueMangaChapterDownloads`)
- `src/lib/offline/use-offline.ts:190` (`useSaveAnimeEntryOffline`)
- `src/lib/offline/use-offline.ts:214` (`useSaveMangaEntryOffline`)
- `src/lib/offline/use-offline.ts:377` (`useRefreshOfflineEntryPayload`)
- `src/lib/offline/download-entry-snapshot-store.ts:80` (`saveAnimeDownloadEntrySnapshot`)
- `src/lib/offline/download-entry-snapshot-store.ts:91` (`saveMangaDownloadEntrySnapshot`)
Concrete failure scenario: for any AniList entry whose `coverImage.large` is null/missing but
`coverImage.extraLarge` is populated, the downloaded-anime info, offline-saved-entries list, and
download-entry snapshot all persist `coverImageUrl: undefined` instead of falling back to the available
`extraLarge` URL, producing a blank/placeholder cover thumbnail across the downloads library, offline
bookmarks list, and offline fallback-entry synthesis — while the exact same entry renders a normal cover
everywhere else in the app (online AniList queries use the full object, not this single degraded field).
Severity kept low since AniList populates `large` whenever `extraLarge` is present in the overwhelming
majority of cases (real-world trigger is rare, but the code is unambiguously wrong and cheap to fix in one
sweep — same copy-paste bug landed in 4 independent files, likely from a shared snippet/pattern that was
never fixed at the source).

### F3 — `getTotalMangaDownloadSize()` always returns 0, dead code (low, smell)
`src/lib/downloads/manga-download-store.ts:449-451`:
```ts
export function getTotalMangaDownloadSize(): number {
    return 0
}
```
Exported but grepped zero call sites anywhere else in the tree. The function that's actually used for manga
disk-usage reporting is `getMangaDownloadDiskUsage()` in `manga-download-manager.ts` (computes real
`Directory.size`). Root cause: `DownloadedMangaChapter` has no `fileSize` field at all (unlike
`DownloadedEpisode`), so a store-level sum was never possible to implement correctly; the stub was left
in place. No live bug since nothing calls it — flagged as dead-code smell only.

## Near-misses investigated and rejected (with reasoning)

1. **"processQueuedAnimeDownload marks a partial/interrupted download as completed if destFile exists"**
   — REJECTED. Read the full Swift native module (`ExpoDownloadManagerModule.swift`). iOS background
   `URLSession` downloads only materialize the file at the JS-visible destination path via an atomic
   `fileManager.moveItem(at:to:)` inside `didFinishDownloadingTo`, which only fires on full completion.
   Partial/in-progress downloads live in a system temp location invisible to the JS/file-path checks used
   by `destFile.exists && fileSize > 0`. Same reasoning applies to the analogous per-page check in
   `downloadChapterPages` (manga). Not a bug.

2. **"useOfflineSyncService (mutation queue drain) is never mounted — dead code"** — REJECTED, initial
   false positive. `grep -rn "OfflineSyncService" src/` only surfaces the definition because the Expo
   Router `app/` directory lives at the REPO ROOT (`H:/Projects/seanime-tenji/app/`), not under `src/app/`.
   Confirmed properly mounted in `app/(app)/_layout.tsx`'s `BackgroundServices` component alongside all
   other offline/download background hooks (`useDownloadSnapshotRefreshService`, `useServerLocalSyncService`,
   `useDownloadQueueResumeService`). Lesson applied for the rest of the sweep: verify call-sites against the
   WHOLE repo, not just `src/`, before flagging anything as unmounted/dead.

3. **`saveServerLocalAnimeRecords` silently no-ops when `currentMeta.identity.key !== identity.key && !completeRefresh`**
   (server-local-store.ts ~line 168) — investigated as a possible data-loss bug (new-identity records silently
   dropped on an incremental/non-complete sync). REJECTED as a real bug after tracing the only caller
   (`server-local-sync-service.ts` `refresh()`): all READ paths (`getServerLocalAnimeRecords`,
   `getServerLocalAnimeRecord`) already gate on `meta.identity.key === identity.key`, so if the identity
   changed (different server/dataDir), the old stored catalog is already unreadable/invisible regardless of
   this guard. The guard exists specifically to stop an in-flight incremental refresh for a STALE identity
   from partially clobbering a newer identity's meta pointer — correct safety behavior, not a bug. Verified
   `completeRefresh = failedMediaIds.length === 0` in the sync service, and traced that a genuinely-removed
   local anime (no longer in `Anime_LocalFile[]`) is excluded from `pathsByMediaId` entirely (never enters
   `fetchRecordsC`), so it does NOT count against `failedMediaIds` and does get correctly pruned on the next
   clean refresh — no orphan-record leak in the common case. One single flaky per-entry fetch blocking
   pruning for ALL entries that cycle is a conservative-but-reasonable tradeoff (explicit log message
   "Preserved stale records after N entry fetch failures" documents the intent) — not flagged.

4. **OTA flow (`src/lib/ota/updates.ts`) — apply/rollback/partial-download safety** — no bugs found.
   Uses stock `expo-updates` APIs (`checkForUpdateAsync`, `fetchUpdateAsync`, `reloadAsync`) which handle
   atomicity/rollback internally (partial fetches don't become the active update; `isRollBackToEmbedded` is
   explicitly checked and short-circuits the prompt in both the auto-check and manual-check paths). Confirmed
   `OtaUpdatePrompt` is mounted exactly once at `app/_layout.tsx:122` (not dead code, not double-mounted).
   Dismissal is keyed by `updateId` and persisted (`DISMISSED_OTA_ID_KEY`), so remounts don't re-nag for an
   already-dismissed update. No timeout wrapping `checkForUpdateAsync`/`fetchUpdateAsync`, but these are
   expo-updates' own network calls (not the app's `requests.ts` fetch wrapper covered by prior fix T4), and a
   hang here only delays a non-blocking background check — considered not reportable (no concrete user-facing
   failure scenario beyond "the check takes a while", and it's fully cancelled via the `cancelled` flag /
   effect cleanup on unmount).

5. **Offline fallback entry has no `listData` (progress/status/score)** — `offline-entry-resolver.ts`
   `buildFallbackAnimeMedia`/`buildFallbackMangaMedia` synthesize a minimal entry from
   `DownloadedAnimeInfo`+`DownloadedEpisode[]` when no snapshot/offline-entry/cached-query-data exists. This
   only fires as the last resort in the fallback chain and produces a functionally-degraded (but not
   crashing/incorrect) view. Judged to be a feature/parity gap ("offline library card doesn't show your
   AniList progress in the rare last-resort fallback path") rather than a correctness bug — excluded per
   task scope (a).

6. **`getPageWorkerCount` submits ALL pending pages as workers on iOS when `backgroundDownloading` is on**
   — read the code comment explaining this is intentional ("ios background sessions need every page task
   submitted before js is suspended"). Not flagged — deliberate design constrained by iOS background
   execution semantics, not a bug.

7. **`drainMutationQueue` drops a mutation after `retryCount >= 5`** — by-design (documented in the queue's
   own logic, "Stops on first failure to preserve ordering"; retry-then-drop prevents an unprocessable
   mutation from permanently blocking the whole queue). Not flagged as a bug; no evidence it silently loses
   data the user isn't informed about (sync-service.ts surfaces `remaining > 0` via a warning log, though
   arguably a toast would be nicer for a silently-dropped mutation after 5 retries — considered, but this is
   a UX-polish suggestion without a concrete "wrong behavior" scenario, so kept out per evidence discipline).
