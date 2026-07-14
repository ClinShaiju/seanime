# Tenji perf crosscut — raw notes

Scope: repo-wide performance crosscut (skim hot paths). List virtualization props, image
loading/caching, memoization of hot components, app-root providers/startup work, heavy
top-level imports, JS-thread animations that should be on native driver/reanimated.

Excluded per task: feature-parity gaps, known-deferred parity items, by-design issues
(pause-on-lock, nakama same-account collision, EAS Windows build), anything already in
tenji-audit.md §0 ledger (T1-T14, all fixed as of v0.1.22 — ledger read in full first).
Server repo (H:/Projects/seanime) used only for cross-checking endpoint semantics, not audited.
Android dirs out of scope (iOS-only shipped target) — except cross-platform files that
happen to contain Android-gated logic with iOS behavioral consequences (flagged below).

## Files read (full or targeted)

- app/_layout.tsx, app/(app)/_layout.tsx
- app/(app)/(tabs)/(library)/index.tsx
- app/(app)/(tabs)/discover/index.tsx (691 lines)
- app/(app)/(tabs)/schedule/index.tsx (722 lines, full)
- app/(app)/(tabs)/(profile)/my-lists.tsx (476 lines, full)
- src/components/features/media/media-entry-grid.tsx
- src/components/features/media/horizontal-media-card-list.tsx
- src/components/features/media/media-entry-card.tsx (355 lines, full)
- src/components/features/anime/prewarm-badge.tsx (50 lines, full)
- src/components/features/anime/episode-card-list.tsx (140 lines, full)
- src/components/features/anime/episode-card.tsx (104 lines, full)
- src/components/shared/carousel.tsx (142 lines, full)
- src/components/shared/sea-image.tsx (75 lines, full)
- src/components/features/discover/discover-hero-carousel.tsx (498 lines, full)
- src/atoms/server.atoms.ts (full)
- src/atoms/anilist-collection.atoms.ts (full)
- src/lib/query-persistence.ts (154 lines, full)
- src/lib/connection-state.ts (157 lines, full)
- src/lib/offline/sync-service.ts (61 lines, full)
- src/lib/offline/download-snapshot-refresh-service.ts (135 lines, full)
- src/lib/downloads/download-queue-resume-service.ts (76 lines, full)
- src/api/hooks/debrid.hooks.ts (useDebridPrewarmStatus)
- src/api/hooks/manga.hooks.ts (useGetMangaLatestChapterNumbersMap)
- src/api/client/requests.ts (full, useServerQuery/useServerMutation/timeout logic)
- src/components/features/manga/reader/manga-reader-screen.tsx (1418 lines, full)
- src/components/features/manga/reader/use-manga-reader-android-long-strip.ts (full)
- tenji-audit.md (ledger, full — read first to avoid re-reporting T1-T14)

Not read in full (crosscut-level judgment call — no FlatList/FlashList/heavy-render
red flags surfaced via targeted grep/skim, and time budget favored depth on the three
findings below): manga-reader-zoom-surface.tsx, manga-reader-utils.ts, manga-reader-layout.ts,
manga-reader-state.ts, anime-entry-library-view.tsx, downloaded-media-shelf.tsx,
anime-entry-server-local-view.tsx, anime-entry-info-view.tsx, manga-entry-info-view.tsx,
server-local-anime-list.tsx, discover/search.tsx, (manga)/index.tsx, (media)/media-list.tsx,
library-hero-carousel.tsx.

Heavy top-level imports check: grepped for `import ... from "lodash"` (full-package import,
not tree-shaken) across src/ — only hit is media-entry-header.tsx (not further chased, single
file, low-traffic screen). No moment.js, no other full-package-import smells found via spot
checks of _layout files' import lists (all use scoped subpath imports: `lodash/sortBy`, etc.,
as seen in schedule/index.tsx `import sortBy from "lodash/sortBy"`).

## Findings (see StructuredOutput for the formal versions)

### F1 — Manga reader "full zoom" long-strip path has no page-count cap and no image
windowing on iOS (HIGH, perf/crash-risk)

`manga-reader-screen.tsx:212-214` gates the path purely on aspect-ratio heuristics:
```
const useFullLongStripZoom = settings.readingMode === MANGA_READING_MODE.LONG_STRIP
    && !longStripLayoutProfile.hasMissingDimensions
    && !longStripLayoutProfile.hasVeryTallPages
```
No `pages.length`/`spreads.length` cap anywhere near this gate (grepped the whole file).

When true, `manga-reader-screen.tsx:686-708` renders every spread via a plain `.map()`
inside a non-virtualized `MangaReaderZoomSurface`/ScrollView — the comment at line 712
even names the tradeoff ("the virtualized path trades continuous zoom for smoother tall
chapter scrolling"), but the alternative virtualized path is only taken when the aspect-ratio
heuristics fail, not based on chapter length.

The one apparent mitigation, `renderImages={shouldRenderLongStripImages(index)}` (line 701),
is a no-op on iOS: `use-manga-reader-android-long-strip.ts:50-53`:
```
const shouldRenderLongStripImages = React.useCallback((spreadIndex: number) => {
    if (!isAndroidLongStrip) return true
    return Math.abs(spreadIndex - currentSpreadIndex) <= ANDROID_LONG_STRIP_IMAGE_WINDOW
}, [currentSpreadIndex, isAndroidLongStrip])
```
`isAndroidLongStrip = readingMode === LONG_STRIP && Platform.OS === "android"` (line 34) —
so on iOS (the only shipped target) this always returns `true`. There is no windowing
mechanism guarding this path on iOS at all.

Concrete failure scenario: a long-strip/webtoon-style chapter with normal (non-extreme)
aspect ratios and known dimensions — i.e. exactly the common case, not an edge case — and
100+ pages (long-strip chapters routinely run into the hundreds of pages) mounts every
page's full-resolution `<Image>` simultaneously with zero virtualization on iOS. Native
image memory grows unbounded with chapter length; on lower-memory iOS devices this is a
plausible OOM/crash vector, not just jank.

Rejected as too speculative to raise further: exact memory numbers (would need device
profiling, out of scope for a static skim) — kept at HIGH given the crash mechanism is
structurally sound (unbounded simultaneous native image mounts) even without an instrumented
number.

### F2 — PrewarmBadge duplicates the pre-T9 anti-pattern as an un-narrowed child (MEDIUM, perf)

`src/components/features/anime/prewarm-badge.tsx` (full, 50 lines):
```tsx
export function PrewarmBadge({ mediaId, episodeNumber, className }: PrewarmBadgeProps) {
    const serverStatus = useServerStatus()
    const enabled = !!serverStatus?.debridSettings?.enabled && !!serverStatus?.debridSettings?.preloadNextStream
    const { data } = useDebridPrewarmStatus(enabled)
    const match = React.useMemo(() => {
        if (!enabled || !mediaId || !episodeNumber || !data) return undefined
        return data.find(it => it.mediaId === mediaId && it.episodeNumber === episodeNumber)
    }, [enabled, data, mediaId, episodeNumber])
    if (!match) return null
    const hot = !!match.metadata
    return (
        <View className={cn("h-8 w-8 items-center justify-center rounded-full border-2 border-black/40",
            hot ? "bg-[#940a00]" : "bg-[#c24e00]", className)}>
            <Flame size={20} color="rgba(255,255,255,0.92)" />
        </View>
    )
}
```
Not wrapped in `React.memo`. Calls `useServerStatus()` directly (the full `Status` object)
instead of a narrow derived atom, unlike the established fix pattern in
`src/atoms/server.atoms.ts:95-105` (`hideAudienceScoreAtom`/`blurAdultContentAtom`/
`showAnimeUnwatchedCountAtom`, consumed via `useMediaCardDisplaySettings()` — this is
literally the T9 fix, commit d97f224).

Rendered once per visible card in `media-entry-card.tsx` (inside the now-memoized
`MediaEntryCard`) and once per visible item in `episode-card-list.tsx`'s renderEpisodeCard.
Because `PrewarmBadge` has its own independent atom subscription, the parent's
`React.memo` cannot shield it — any `serverStatusAtom` write (e.g. one unrelated settings
field changing, or the 60s status poll landing with any real diff) re-renders every
visible `PrewarmBadge` instance across every rendered list simultaneously. Same class of
bug T9 fixed for the card itself, left unaddressed in this shared child.

### F3 — Manga reader page cells re-render on every HUD tap (MEDIUM, perf)

`manga-reader-screen.tsx:747-764`, the horizontal paged `FlashList`'s `renderItem` is an
inline arrow function (not `useCallback`-wrapped):
```tsx
renderItem={({ item }) => (
    <ReaderPagedItem
        ...
        onTap={toggleControlsVisible}
        tapExclusionBottom={hudTapExclusionBottom}
        tapExclusionTop={hudTapExclusionTop}
        ...
    />
)}
```
The vertical virtualized long-strip path uses a `useCallback`-wrapped
`renderVirtualizedLongStripItem` (line 727), but its dependency array includes
`hudTapExclusionTop`/`hudTapExclusionBottom` (lines 596-597), which are themselves derived
directly from `controlsVisible` (lines 169-170: `controlsVisible ? insets.top + 112 : 0`)
— so that callback is *also* recreated on every HUD toggle despite the `useCallback`.

Neither `ReaderPagedItem` (line 1130) nor `ReaderLongStripItem` (line 1036) is wrapped in
`React.memo` (grepped both definitions in the file — no `React.memo(...)` wrapper on
either).

Concrete scenario: tapping the screen to toggle the reading-controls HUD — the single most
common gesture while reading (used to check page count, exit, adjust settings) — flips
`controlsVisible`, which recreates the renderItem (horizontal path) or its effectively-changed
`useCallback` (vertical path), which FlashList uses to re-render all currently-visible page
cells, which are unmemoized components holding full-resolution zoomable `<Image>`s. Every
tap forces unnecessary re-render work on the images currently on screen. Not a crash, but a
plausible source of visible jank/flicker on a very hot, latency-sensitive interaction,
especially mid pinch-zoom-adjacent gesture state.

## Near-misses considered and rejected/downgraded

- **Shared-queryKey-per-card-instance refetch pattern** (`useDebridPrewarmStatus`,
  `useGetMangaLatestChapterNumbersMap`): both use a single global `queryKey` (not
  parameterized by media/episode), and the app's `QueryClient` default is `staleTime: 0`
  with no `refetchOnMount` override (app/_layout.tsx), so default `refetchOnMount: true`
  applies. Because grid/list virtualization windows unmount and remount cards during fast
  scrolling (confirmed FlatList `windowSize`/`removeClippedSubviews` in schedule, my-lists,
  media-entry-grid), each remounted card's badge mounts a fresh observer on an
  always-stale query, triggering a background refetch. React Query does dedupe truly
  simultaneous fetches to the same key, so this isn't a burst-multiplier, but it is a
  legitimate steady drip of duplicate network calls while scrolling. Considered folding
  into F2 but kept separate in my head — decided NOT to raise as a 4th standalone finding
  since the traced scenario (scroll-triggered remounts + staleTime:0) has a plausible but
  not fully certain magnitude (depends on how aggressively FlatList's window actually
  unmounts cells vs. just visually clips them), and F2 already covers the same
  `PrewarmBadge`/`useServerStatus` subscription surface with a more airtight trace. Not
  reported as a separate finding to avoid diluting signal; flagged here for the record.

- **`key={index}` on `<MediaEntryCard>` inside `horizontal-media-card-list.tsx`
  `renderItem`**: redundant since `FlatList` uses `keyExtractor`, not element `key`, for
  reconciliation identity — inert, no observable perf effect. Not reported.

- **`hydrateQueryClient()` / `setupQueryPersistence()` running synchronously at
  `app/_layout.tsx` module top-level**, iterating `getAllDownloadedAnime()`/
  `getAllDownloadedManga()`: read `query-persistence.ts` in full looking for an
  O(n) startup-cost issue proportional to offline library size. No evidence of a
  concrete threshold being crossed (MMKV reads are synchronous/fast, and the iteration
  is a simple hydration loop, not N network calls or N JSON.parse of large blobs) — capped
  at "unverified, plausibly fine," not raised as a finding.

- **FlashList vs FlatList dual virtualization system**: already flagged as a known,
  non-urgent "minor" note in tenji-audit.md §3 ("converge when convenient") — not
  re-reported per the exclusion rule for already-ledgered items.

- Majority of list/grid components surveyed (library, discover sections, media-entry-grid,
  horizontal-media-card-list, carousel, episode-card-list, schedule's ScheduleGrid/
  UpcomingEpisodesRow, my-lists' FlatList) all have sensible `getItemLayout`, `keyExtractor`,
  `windowSize`, batching props, and (where applicable) `React.memo` on row/card components —
  consistent with the ledger's "big wins already in place" note. No virtualization
  misconfiguration found.

- `discover-hero-carousel.tsx`: all animation via reanimated worklets (UI thread, not JS
  thread), deferred image mount via `InteractionManager.runAfterInteractions` +
  `HERO_IMAGE_MOUNT_DELAY_MS`, `cachePolicy="disk"` + `priority="low"` + `allowDownscaling`
  on backdrop images — well-optimized, no issue.

## Coverage summary

Read every file explicitly named in the task's checklist categories that exists in the
repo (list virtualization, image loading, memoization of hot components, app-root
providers/startup, manga reader virtualization trade-off) plus the full manga reader
screen and its Android-long-strip support hook (chased because it directly bears on F1).
Did not open the remaining tail of screen-level files listed above under "Not read in
full" — spot-checked via grep for FlatList/FlashList/heavy computation and found nothing
that redirected attention there; given the crosscut's explicit "skim hot paths" framing
and a 3-finding yield already meeting the evidence bar, judged further exhaustive reads
of those files as low expected value relative to time cost. If a stricter "every file"
reading is required, flag for a follow-up pass — the specific files are enumerated above.
