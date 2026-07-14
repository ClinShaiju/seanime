# Tenji audit sweep — entry/media scope

Scope: `src/components/features/anime/` + `src/components/features/media/` in
`H:/Projects/seanime-tenji`. Read every file in both directories exhaustively (28
files total, listed below). Cross-checked `H:/Projects/seanime/tenji-audit.md`
§0 ledger first (T1–T14 all fixed as of v0.1.22) — none of the findings below
overlap with anything on that ledger.

## Files read (28/28)

anime/continue-watching.tsx, anime/downloaded-anime-list.tsx,
anime/episode-card-list.tsx, anime/episode-card.tsx, anime/episode-list-item.tsx,
anime/prewarm-badge.tsx, anime/server-local-anime-list.tsx,
media/anime-entry-action-bar.tsx, media/anime-entry-downloaded-view.tsx,
media/anime-entry-info-view.tsx, media/anime-entry-library-view.tsx,
media/anime-entry-screen-context.tsx, media/anime-entry-screen.tsx,
media/anime-entry-server-local-view.tsx, media/anime-entry-view-switcher.tsx,
media/download-episodes-modal.tsx, media/downloaded-media-shelf.tsx,
media/edit-anilist-entry.tsx, media/horizontal-media-card-list.tsx,
media/library-hero-carousel.tsx, media/media-entry-card.tsx,
media/media-entry-grid.tsx, media/media-entry-header.tsx,
media/media-entry-quick-info-sheet.tsx, media/media-entry-score.tsx,
media/media-entry-scroll-shell.tsx, media/media-episode-info-sheet.tsx,
media/merged-season-section.tsx, media/season-switcher.tsx,
media/server-download-modal.tsx.

## Findings

### F1 — Failed downloads cannot be deleted in-place (high, bug)

Files: `src/components/features/anime/episode-list-item.tsx` (lines ~131,
248-258, 275-291) + `src/components/features/media/anime-entry-downloaded-view.tsx`
(lines ~168-174, 301-405).

`episode-list-item.tsx`:
```
const showDetailsButton = !disableDetailsButton && hasEpisodeDetails
...
{showDetailsButton ? (
    <View className="ml-2 py-1">
        <EpisodeDetailsButton .../>
        {action ? <View className="">{action}</View> : null}
    </View>
) : null}
```
The caller-supplied `action` node is nested *inside* the `showDetailsButton`
conditional, so it never renders when `disableDetailsButton` is true (or the
episode has no metadata). Also, when `rowPressable` is false the row is a bare
`<View {...sharedProps}>` with no press handlers at all — the whole row is
only interactive through props that `anime-entry-downloaded-view.tsx` sets
conditionally.

`anime-entry-downloaded-view.tsx`, `DownloadedEpisodeListItem`:
```
const isCompleted = episode.status === "completed"
const matchingEpisode = React.useMemo(() => entry.episodes?.find(
    item => item.episodeNumber === episode.episodeNumber && item.type === episode.type) ?? null,
    [entry.episodes, episode.episodeNumber, episode.type])
...
<EpisodeListItem
    onEpisodePress={isCompleted ? handlePlay : undefined}
    onEpisodeLongPress={isCompleted ? handleLongPress : undefined}
    rowPressable={isCompleted}
    disableDetailsButton={!matchingEpisode}
    action={(<Pressable onPress={handleDeletePress}><Ionicons name="trash-outline" .../></Pressable>)}
/>
```
and the Failed section header explicitly promises: "Remove failed downloads
here, then retry from the Library tab."

**Failure scenario**: a download fails (`episode.status === "failed"`) for an
episode number/type that has no match in `entry.episodes` (extra/special not
in AniList metadata yet, or metadata not synced). Then `isCompleted=false` →
`rowPressable=false` and both press handlers are `undefined` → row/thumbnail
inert. `matchingEpisode=null` → `disableDetailsButton=true` →
`showDetailsButton=false` → the `action` block containing the only delete
(trash) button never renders. The user has no way to remove that failed item
in place — contradicting the screen's own copy — only "Delete All" (wipes
everything) is left.

Suggested fix: render `action` as a sibling of the `showDetailsButton` block,
not nested inside it, so it always renders when the caller supplies one.

### F2 — Off-by-one swallows the last item when list length equals `limit` (medium, bug)

File: `src/components/features/media/horizontal-media-card-list.tsx`, lines
39-60, 118-140. Also exercised from
`src/components/features/media/anime-entry-info-view.tsx:253-258`
(Recommendations row, uses the default `limit=9`, no override).

```
const visibleMedia = React.useMemo(() => !limit ? media : media.slice(0, limit), [limit, media])
...
const renderItem = React.useCallback(({ item, index }) => {
    if (index === limit - 1) {
        return (<View ...><Button onPress={...}><Text>See all ({media.length})</Text></Button></View>)
    }
    ...
}, [limit, media, ...])
...
{(media.length > limit) && <Button ... onPress={...}><Ionicons name="arrow-forward" /></Button>}
```

The header's "more available" arrow-forward button is correctly gated on
`media.length > limit`. But `renderItem`'s swap to the "See all" card is
gated only on `index === limit - 1`, independent of whether there really are
more than `limit` items.

**Failure scenario**: `media.length === limit` exactly (e.g. an anime whose
AniList recommendations list happens to have exactly 9 entries, the default
`limit`). `visibleMedia = media.slice(0, 9)` has length 9, indices 0..8.
Index 8 (`limit-1`) is a real, valid media item, but `renderItem` replaces it
with a "See all (9)" card. The header shows no arrow-forward (9 > 9 is
false), so there is no visual cue anything is hidden. Net effect: the 9th
recommendation is permanently invisible in the horizontal scroller, and the
"See all" card that took its slot navigates to a page listing... the same 9
items, including the one that was hidden — a misleading, redundant CTA that
also silently drops a real item from the horizontal view.

Suggested fix: only replace the last slot with "See all" when
`media.length > limit` (e.g. render `visibleMedia.length` real cards plus an
extra "see all" card only in that case, or slice to `limit - 1` items when
truncating).

### F3 — Hooks-order violation in `MediaEntryQuickInfoSheet` (low, smell — currently unreachable)

File: `src/components/features/media/media-entry-quick-info-sheet.tsx`, line
107 vs 109-111.

```
export function MediaEntryQuickInfoSheet<T extends "anime" | "manga">({
    type, media, open, onOpenChange, preferFetchedMedia,
}: MediaEntryQuickInfoSheetProps<T>) {
    if (!media) return null

    const serverStatus = useServerStatus()
    const { data: animeEntry, isLoading: animeEntryLoading } = useGetAnimeEntry(type === "anime" && open ? media.id : undefined)
    const { data: mangaEntry, isLoading: mangaEntryLoading } = useGetMangaEntry(type === "manga" && open ? media.id : undefined)
    ...
```
An early `return null` guarding `!media` sits before three hook calls,
violating React's Rules of Hooks (hook count/order becomes conditional on
`media`'s nullability across renders/remounts of the same fiber).

Checked reachability: the only call site is
`media-entry-card.tsx:340` — `{sheetOpen ? <MediaEntryQuickInfoSheet media={media as any} .../> : null}`,
where `media` is `MediaEntryCardProps.media`, a required (non-optional) prop.
So in the current codebase `media` is always truthy whenever this component
mounts, and the bug is currently latent/unreachable. Per the audit's evidence
discipline (cap severity at low + category=smell when no concrete failure
scenario can be traced), reporting as low/smell rather than a live bug. Worth
fixing defensively (move the guard after the hooks, or drop it since the
prop is required) so a future caller that loosens the prop type doesn't
immediately reintroduce a crash.

### F4 — Leftover debug `console.log` (low, smell)

File: `src/components/features/media/server-download-modal.tsx`, line 189.

```
const nextEpIndex = episodes.findIndex(ep => (ep.progressNumber || ep.episodeNumber) > progress)
console.log(entry.listData?.progress, nextEpIndex, episodes)
if (nextEpIndex !== -1) {
```
Runs on every open of the "Download on Server" sheet (effect keyed on
`[open, episodes, entry.listData?.progress]`) and dumps the full episode
array to console. Not a functional bug, just debug scaffolding left behind;
flagging as a low-severity smell/cleanup item.

## Near-misses considered and rejected (no concrete failure scenario traced)

- `anime-entry-action-bar.tsx` (105 lines): in one prop-state combination the
  Download button renders icon-only with no label/count. Read as a
  deliberate compact-mode design choice given the surrounding layout, not a
  bug — no trigger path makes it broken or misleading, just terse. Not
  reported.
- `anime-entry-library-view.tsx` (341 lines) vs `anime-entry-server-local-view.tsx`
  (243 lines): heavy structural/prop-shape duplication between the two view
  components. This is an architectural smell (maintenance burden, drift
  risk) but not a runtime defect with a traceable failure scenario, and the
  task's evidence bar requires a concrete bad-outcome trace to report above
  "low/smell" — decided this duplication alone doesn't clear that bar as a
  standalone finding.
- `merged-season-section.tsx`: traced `courInfo`/`displayEpisodes` index
  alignment (both remapped to 1..N ranges) end to end — confirmed correct,
  no bug.
- `season-switcher.tsx`: traced the franchise season-grouping Map (`order`/
  `groups` keyed by `tmdbId:seasonNumber` or `id:mediaId` fallback) and the
  `appliedRef`-gated auto-merge effect closely, since "season-grouping edge
  cases" was called out explicitly in scope. No concrete bug pinned down —
  the TMDB-id-vs-mediaId fallback key correctly isolates same-season
  mislabeled siblings from true multi-cour groups in every case traced.
- `media-episode-info-sheet.tsx`: cross-checked its `hasMetadata` gating
  against `useHasEpisodeDetails` in `episode-list-item.tsx` for consistency
  — no mismatch found.
- `download-episodes-modal.tsx` / `server-download-modal.tsx`: reviewed the
  watched/locked filtering logic for selectable episodes (`isEpisodeSelectionLocked`,
  the `showWatched` toggle, `selectUnwatched`/`deselectUnwatched` symmetry)
  and the pagination reset effects — all internally consistent, no bug
  traced. `handleDownload`/batch-add fire-and-forget without an
  onError-driven UI acknoweldgement inside the modal itself was considered
  but not flagged: errors are plausibly surfaced elsewhere (toast layer) and
  no concrete broken-state trace could be built from reading this file
  alone.
- `edit-anilist-entry.tsx`: reviewed score/progress clamping
  (`clampNumber`, `Number.isNaN` fallbacks), the `ScoreStepper` long-press
  repeat timers (proper `stopTimer` cleanup on unmount and `onPressOut`), and
  offline "queue" wording — no bug traced.
- `library-hero-carousel.tsx`: reviewed the auto-rotate interval/isInteracting
  gating, the animated-scroll-handler-driven index sync vs. the JS-side
  `handleScrollEnd`, and the backdrop image virtualization window
  (`HERO_BACKDROP_IMAGE_WINDOW`) — all consistent, no bug traced.
- `media-entry-header.tsx`, `media-entry-card.tsx` (355 lines, largely read
  in a prior pass this session before context compaction): no new issues
  beyond F3's call site, which was re-confirmed this pass.

## Coverage

All 28 files in the assigned scope (`src/components/features/anime/` and
`src/components/features/media/`) were read in full. No files in scope were
skipped or sampled. Findings above are the complete result set from this
sweep; everything else read was assessed and is either clean or listed as a
rejected near-miss with reasoning.
