# Tenji sweep: onlinestream / torrentstream / directstream-mediastream player flow

Scope (as assigned): `src/components/features/onlinestream/`, `src/components/features/torrentstream/`,
and directstream/mediastream hook usage in the player flow. Repo: `H:/Projects/seanime-tenji`.

Ledger `H:/Projects/seanime/tenji-audit.md` skimmed first — T1–T13 all fixed as of v0.1.22; none of that
ledger's areas overlap what's reported below (no re-flagging of T13/prewarm dedup, etc).

## Files read in full

### `src/components/features/onlinestream/`
- `anime-entry-onlinestream-section.tsx` (340 lines) — main section UI, play-firing effects. **Finding A here.**
- `onlinestream-manual-match-modal.tsx` (169 lines) — provider search/mapping modal. No findings.
- `use-onlinestream-controller.ts` (255 lines) — provider/server/quality atoms, episode-source query,
  quality-fallback chain, `requestPlay`/`cancelPlayRequest`. No findings (this file is actually the
  reference for how the falsy-check bug in Finding A should have been written — it does it correctly).

### `src/components/features/torrentstream/`
- `anime-entry-torrent-stream-section.tsx` (287 lines) — loading/active banners, episode list, picker sheet host.
  Read fully; playbackIntent effect (torrentstream-auto-select/-previous-batch, debridstream-*) traced,
  no null/0 pitfalls (uses `.find()` + bail-if-not-found, no truthiness shortcuts on episode numbers).
- `use-debrid-prewarm.ts` (54 lines) — canonical prewarm hook (ledger T13). No findings.
- `torrent-stream-view.tsx` (164 lines) — presentational episode-list wrapper. No findings.
- `use-torrent-stream-controller.ts` (987 lines) — stream-mode switching, provider selection persistence,
  search variables, previous-batch reuse (`getPreviousBatchSelection`, `guessPreviousBatchFileIndex`),
  `startAutoSelectedStream`/`startManualStream`, `handleEpisodePress` routing, single-file auto-select
  220ms timer (verified cancelled correctly via `sheetStage` dependency + `resetPicker`).
  `episodeSelectionLockedRef` synchronous-lock pattern checked against the atom-derived value — consistent.
  No confirmed bug found despite a targeted second pass on the previous-batch offset arithmetic.
- `torrent-stream-picker-sheet.tsx` (1438 lines) — release picker bottom sheet (selection/file/provider
  stages), `TorrentCard`, `TorrentMetadataTags`, `MetaTag`, debrid instant-availability ("Cached") badge/filter.
  **Finding B here.** Debrid instant-availability threading (lines ~499-536) read carefully — `isCached`,
  `cachedCount`, `showCacheFilter`, `visibleTorrents` filter all correct; `showCacheFilter` correctly gated
  to `streamMode === "debrid"`. `TorrentSearchQueryField` debounce (focus-tracked draft state, 180ms debounce,
  blur/submit-commit) correct.

### directstream / mediastream hooks
- `src/api/hooks/directstream.hooks.ts` (26 lines) — thin wrappers (`DirectstreamPlayLocalFile`,
  `DirectstreamConvertSubs`). Not used by onlinestream/torrentstream flow; local-file playback territory
  (different agent's scope). No findings.
- `src/api/hooks/mediastream.hooks.ts` (65 lines) — thin wrappers (settings get/save, request/preload
  container, shutdown transcode). Same as above — local-file/mediastream-transcode territory, not
  onlinestream/torrentstream. No findings.

### `src/lib/player/` (player-flow plumbing feeding the in-scope sections)
- `playback-coordinator.ts` (180 lines) — `usePlaybackCoordinator`; `playOnlineStreamEpisode` (the function
  called from Finding A's effect) traced end-to-end into `toSourceFromOnlineStream` → external-player-or-
  internal `startOnlinePlayback`. No bug in this file itself.
- `source-resolver.ts` (38 lines) — `toSourceFromOnlineStream`. Straightforward HLS/HTTP detection
  (`videoSource.type === "m3u8" || url.endsWith(".m3u8")`) and `MobilePlaybackSource` construction.
  No findings.
- `index.ts` (57 lines) — barrel re-exports only. No findings.
- `debrid-reconnect.ts` (55 lines) — `useDebridReconnectResume`, arms on drop-while-active, fires once on
  reconnect. Logic internally consistent; no concrete failure scenario traced (would need live two-device
  reconnect testing to falsify, out of static-review reach) — not reported per evidence-discipline rule.
- `session.ts` (819 lines) — atoms (`torrentStreamPendingInfoAtom`, `streamSessionModeAtom`,
  `activeStreamSessionAtom`, `torrentStreamIsPreparingAtom`, `torrentStreamLoadingStateAtom`,
  `debridStreamStateAtom`, etc.), `resolvePlaybackMetadataFromCache` (multi-tier cache fallback — silent
  break on first partial match per tier, theoretically could pick a stale entry under duplicate mediaIds
  across sources, but no concrete reproducing scenario, not reported), `usePlayerEventListener` (central ws
  handler for `torrentstream-state`/`debrid-stream-state`/`external-player-open-url`, including the 500ms
  `loadingFallbackTimer` — traced `clearLoadingFallback()` call sites, did not find an unclosed path but also
  did not fully exhaust every branch; no concrete failure scenario constructed, so not reported),
  `useCleanupPlaybackSession` (resets atoms on unmount; functional-update comment shows deliberate care
  around stale-closure bugs in this exact area). No findings meeting the evidence bar.
- Deliberately NOT read in full (out of declared scope — general player UI / local-file / continuity, not
  onlinestream/torrentstream selection flow): `use-mpv-player.ts` (584 lines), `use-continuity-sync.ts`
  (138 lines), `external-players.ts` (165 lines), `player-preferences.ts` (169 lines),
  `server-local-source.ts` (122 lines), `local-file-source.ts` (80 lines), `subtitle-search.ts` (218 lines),
  `types.ts` (159 lines, only referenced for type shapes while reading the above). Flagging this boundary
  explicitly so overlap with another agent's player-UI sweep is clear.
- Not read (backing API hooks one layer below the controllers already read in full — the controllers'
  consumption of these was verified, the hook implementations themselves were not independently audited):
  `debrid.hooks.ts`, `torrentstream.hooks.ts`, `onlinestream.hooks.ts`, `torrent_search.hooks.ts`.

## Findings

### Finding A — episode 0 is a silent playback dead-end in onlinestream (HIGH, bug)
`src/components/features/onlinestream/anime-entry-onlinestream-section.tsx:49`

```js
const firedPlayRef = React.useRef<string | null>(null)
React.useEffect(() => {
    if (!controller.playRequestedEpisode) return   // <-- falsy check treats 0 same as null
    if (!controller.selectedVideoSource) return
    if (controller.isLoadingSource) return
    ...
    playOnlineStreamEpisode({ videoSource: controller.selectedVideoSource, episodeNumber: controller.playRequestedEpisode, episode: ep?.metadata })
    controller.cancelPlayRequest()
}, [controller.playRequestedEpisode, controller.selectedVideoSource, controller.isLoadingSource, controller.provider, controller.episodes, playOnlineStreamEpisode, controller])
```

`use-onlinestream-controller.ts` deliberately distinguishes 0 from "no request" everywhere else:
```
const [playRequestedEpisode, setPlayRequestedEpisode] = React.useState<number | null>(null)
...
playRequestedEpisode !== null && !!provider,   // query-enable gate: correctly treats 0 as a real request
```//
So pressing episode 0 (OVA/prologue numbered 0 — a legitimate `episodeNumber` in this domain) sets
`playRequestedEpisode = 0`, the source query is enabled (0 !== null) and resolves a `selectedVideoSource`,
but the play-firing effect's `if (!controller.playRequestedEpisode) return` guard is truthy-false for 0 and
returns immediately every render — `playOnlineStreamEpisode` is never called. No error, no loading spinner
stuck — the episode row just does nothing when pressed. Same pattern is also latent in the
`animeEntryPlaybackIntentAtom` deep-link handler in the same file (lines 80-97), which calls
`controller.requestPlay(playbackIntent.episodeNumber)` — if a deep link targets episode 0, `requestPlay`
succeeds but the same guarded effect still silently swallows it downstream.

**Fix**: `if (controller.playRequestedEpisode === null) return` (or `== null`), matching the hook's own gate.

### Finding B — MetaTag ignores its `tone` prop, breaking Dubbed/Multi-Subs/language badge styling (MEDIUM, ui)
`src/components/features/torrentstream/torrent-stream-picker-sheet.tsx:1213-1231`

```js
function MetaTag({ label, tone = "default", icon }: { label: string; tone?: "default" | "muted" | "subtle" | "indigo"; icon?: React.ReactNode }) {
    tone = "muted"
    const style = tone === "muted"
        ? { bg: "transparent", color: "rgba(255,255,255,0.55)" }
        : tone === "subtle"
            ? { bg: "rgba(255,255,255,0.06)", color: "rgba(255,255,255,0.92)" }
            : tone === "indigo"
                ? { bg: "#a5b4fc", color: "#111827" }
                : { bg: "transparent", color: "rgba(255,255,255,0.92)" }
    return (
        <View className={cn("rounded-md py-0.5 flex-row items-center gap-1", tone !== "muted" ? "px-1.5" : "pr-1.5")} style={{ backgroundColor: style.bg }}>
            {icon}
            <Text className="text-[11px] font-medium" style={{ color: style.color }}>{label}</Text>
        </View>
    )
}
```

Callers in `TorrentMetadataTags` (same file, ~1268-1289) explicitly pass distinguishing tones:
```js
<MetaTag key={term} label={...} tone="subtle" icon={<Ionicons name="mic" .../>} />         // multi-audio "Original + Dub"
<MetaTag label="Dubbed" tone="indigo" icon={<Ionicons name="mic" .../>} />                  // hasDubs
<MetaTag label="Multi Subs" tone="indigo" icon={<Ionicons name="chatbubble-ellipses" .../>} />  // hasMultiSubs
```
The unconditional `tone = "muted"` on the first line of the function body discards whatever tone the
caller passed before the ternary chain even runs, so every `MetaTag` — video terms, audio terms, and the
`subtle`/`indigo` badges alike — renders identically dim/transparent. The intended visual distinction
between "Dubbed"/"Multi Subs" (should be indigo pill, `#a5b4fc` bg) and plain metadata tags (should be
transparent/dim) is completely lost; a user scanning the release picker for a dubbed release loses the
one piece of UI meant to make it visually pop.

**Fix**: delete line 1214 (`tone = "muted"`).

## Near-misses investigated and rejected (with reasoning)

- `firedPlayRef` referenced in `handleEpisodePress` (anime-entry-onlinestream-section.tsx:43) before its
  `React.useRef` declaration at line 47 (textual order). **Not a bug**: `handleEpisodePress` is a callback
  invoked later (on press), by which point the full component body — including the `useRef` call — has
  already executed; JS closures capture the binding, not a snapshot at definition time, and hooks execute
  top-to-bottom on every render regardless of where in the file a later-declared function happens to
  reference them.
- Single-file auto-select 220ms timer in `use-torrent-stream-controller.ts` (~lines 810-832): could in
  theory fire after the sheet is dismissed. Traced: `sheetStage` is a dependency of the effect and
  `resetPicker()` (called on close) resets it, which re-runs the effect and its cleanup clears the timer.
  No reproducible leak.
- `TorrentSelectionStage`'s local `cachedFilter`/`isFiltersExpanded` state resets if `pickerStage`
  transitions away from `"torrents"` and back (component remounts via the parent's stage ternary). This is
  a real minor UX papercut (user's "Cached only" filter choice or expanded-filters state doesn't survive a
  round trip through the file-selection or provider-selection stage) but severity/impact is low and I could
  not confirm users actually navigate stages backward in the picker's normal flow enough to matter — capped
  below the reporting bar given the exhaustive-but-prioritize-evidence instruction; noting it here rather
  than in structured findings.
- "Uncached" filter chip (~line 771) shows no count while "All (N)" and "Cached (N)" do. Purely cosmetic
  asymmetry; no functional impact, no concrete failure scenario, not reported.
- `const languages: any[] = []` in `TorrentMetadataTags` with the real computation commented out directly
  above it (`// const languages = metadata.language?.length ? [...new Set(metadata.language)] : []`).
  Reads as an intentionally disabled feature (the `languages.length > 2` block below it is consequently
  dead) rather than an accidental break — no `tone` prop mismatch or crash risk, and disabling a
  once-working badge without evidence of *why* it was disabled doesn't meet the "broken half-wired code"
  bar with a traceable failure scenario. Not reported.
- `resolvePlaybackMetadataFromCache` (session.ts) multi-tier lookup does a silent `break` on the first
  partial match per tier; theoretically a duplicate mediaId across cache tiers could resolve to a stale
  entry. No concrete reproducing scenario found (would require two different cached entries under the same
  mediaId simultaneously, which the query-key design elsewhere in the app appears to prevent) — not reported.
- `loadingFallbackTimer` (session.ts, `usePlayerEventListener`, 500ms `TORRENT_STREAM_LOADING_FALLBACK_DELAY`):
  traced `clearLoadingFallback()` call sites across the branches; did not find a definitively unclosed path,
  but also did not exhaustively enumerate every websocket event ordering. No concrete failure scenario
  constructed within the time available — not reported per evidence-discipline rule (cap at "low"/"smell"
  only with a scenario; here there isn't even that).
- `debrid-reconnect.ts`'s reconnect-resume gate is inherently time/race-dependent; read through twice,
  arm/disarm logic looked internally consistent, but falsifying it needs live two-device reconnect testing,
  not static review. Not reported.
- Debrid instant-availability ("Cached") badge threading itself (torrent-stream-picker-sheet.tsx ~499-536):
  read specifically because it was called out in the task brief (0.1.24 addition) — `isCached`, `cachedCount`,
  `showCacheFilter`, `visibleTorrents` all correct and consistent with `debridInstantAvailability`'s generated
  type (`Record<string, Debrid_TorrentItemInstantAvailability>`, presence-keyed). No bug found in the
  threading logic itself (separate from Finding B's styling bug, which affects these badges' *rendering*
  but not their correctness/data).

## Coverage summary

Every file in `src/components/features/onlinestream/` and `src/components/features/torrentstream/` was
read in full (8 component files, ~3600 lines combined). directstream/mediastream hook files were read in
full but are thin wrappers unrelated to the onlinestream/torrentstream play path (they serve local-file
playback, a different agent's territory). Within `src/lib/player/`, the files directly in the
onlinestream/torrentstream play path were read in full (`playback-coordinator.ts`, `source-resolver.ts`,
`index.ts`, `debrid-reconnect.ts`, `session.ts` — 819 lines, the central websocket/atom layer). General
player-UI/local-file/continuity files in the same directory (`use-mpv-player.ts`, `use-continuity-sync.ts`,
`external-players.ts`, `player-preferences.ts`, `server-local-source.ts`, `local-file-source.ts`,
`subtitle-search.ts`) were deliberately left unread as out of declared scope. The four backing API hook
files (`debrid.hooks.ts`, `torrentstream.hooks.ts`, `onlinestream.hooks.ts`, `torrent_search.hooks.ts`)
were not independently read; their consumption via the controllers was verified instead. Two findings were
confirmed with concrete failure scenarios and file:line evidence; several near-misses were investigated and
rejected with reasoning recorded above.
