# Tenji sweep — routes/app scope

Scope: `app/` (all expo-router route files: `(app)`, `(out)`, `(tabs)`, `(media)`, `(library)`,
`(manga)`, `(profile)`, `entry`, `discover`, `schedule`) + `app.config.ts`, `index.js`,
`metro.config.js`, `babel.config.js`. Concerns: route param validation, auth gating of `(app)`
vs `(out)`, deep links, back behavior, modal presentation, screens that mount heavy work, layout
`_layout` files, config sanity, plus general correctness/perf/UX/UI/security within these files.

Excluded per task: feature-parity gaps vs web/Denshi, known deferred parity items, known
by-design issues (pause-on-lock needs passcode, nakama same-account collision, EAS hermesc
Windows crash), anything already in `tenji-audit.md` §0 ledger (T1–T13, all fixed as of v0.1.22),
codegen style in `src/api/generated/`.

## Files read (exhaustive)

Root/config:
- `app/_layout.tsx`, `app/+not-found.tsx`, `app.config.ts`, `index.js`, `metro.config.js`, `babel.config.js`

`(out)`:
- `app/(out)/set-server-url.tsx`

`(app)` layout + background mounts:
- `app/(app)/_layout.tsx`
- `src/api/components/server-data-wrapper.tsx` (ServerUrlWrapper + ServerDataWrapper, used by root/`(app)` layouts — read fully as it's the auth/deep-link gate)

`(app)/(tabs)`:
- `(tabs)/_layout.tsx`
- `(library)/_layout.tsx`, `(library)/index.tsx`
- `(manga)/_layout.tsx`, `(manga)/index.tsx`
- `discover/_layout.tsx`, `discover/index.tsx`, `discover/search.tsx`
- `schedule/_layout.tsx`, `schedule/index.tsx`
- `(profile)/_layout.tsx`, `(profile)/index.tsx`, `account.tsx`, `active-stream.tsx`,
  `download-settings.tsx`, `logs.tsx`, `my-lists.tsx`, `server-downloads.tsx`, `unmatched.tsx`

`(app)/entry`:
- `entry/_layout.tsx`, `entry/anime/[id]/index.tsx`, `entry/manga/[id]/index.tsx`

`(app)/(media)`:
- `(media)/_layout.tsx`, `media-list.tsx`, `anime-download-queue.tsx`, `anime-downloads.tsx`,
  `manga-download-queue.tsx`, `manga-downloads.tsx`, `manga-reader.tsx`, `player.tsx`
  (player.tsx: symbol overview via Serena + targeted full reads of mount/back-nav/render-fallback
  sections; the dense gesture/control-overlay body is component-level, not route-level, and is
  covered by another sweep agent's scope)

Cross-checks:
- `grep -rn "Linking|initialURL|useURL|createURL|prefixes"` across the tree → confirmed no
  deep-link handling exists outside expo-router's built-in `scheme: "seanime"` routing.
- `grep -rn "(media)/player"` across `src/` → confirmed all 5 navigation call sites use
  `router.push`, not `replace`.
- `grep -rn "return null"` and `grep -rn "router.back()|back()"` across `app/` → swept every
  hit to separate route-mount dead-ends from harmless inner-component guards.

Reference read (context only, not in edit/finding scope): `H:/Projects/seanime/tenji-audit.md`.

## Findings (see StructuredOutput for the authoritative list — summarized here with rationale)

### 1. `ServerUrlWrapper` force-redirects away from any deep-linked path on mount — breaks all deep links for returning users
`src/api/components/server-data-wrapper.tsx:15-39`. On every mount/`serverUrl` change, if
`serverUrl` is truthy and `pathname !== "/set-server-url"`, it unconditionally
`router.replace("/(app)/(tabs)/(library)")`s — regardless of what pathname the app actually
launched into. Since `scheme: "seanime"` is declared in `app.config.ts` and expo-router's file
routing is the *only* deep-link mechanism in the codebase (confirmed via grep — no `Linking`/
`useURL`/`createURL` handling exists anywhere else), this wrapper is the sole gate a deep link
passes through, and it clobbers the destination for any user who already has a server configured
(i.e., every returning user — the majority case). A URL like `seanime://entry/anime/123` or a
push-notification deep link would cold-launch the app onto that route, then this effect fires
and immediately bounces to the library tab. Severity: high — this is a full deep-link outage,
not an edge case.

### 2. `media-list.tsx` renders a fully blank screen with no back button if its Jotai atom is unset
`app/(app)/(media)/media-list.tsx:26-31`. Content comes from `__media_listPageContentAtom` (set
by the calling screen before `router.push`), not from route params — in-memory only, not
persisted. `if (!mediaListPageContent) return null` happens before any header/back-button JSX.
Any path that lands on this route without the setter screen having run first (stale restored
navigation state, a hypothetical future deep link, dev-reload with atom reset) produces an
inescapable blank screen — no back button, no message, and (unlike `manga-reader.tsx`) this
route isn't excluded from `gestureEnabled` at the `(media)` layout level (media-list still has
swipe-back), so it's marginally recoverable via iOS swipe-back only — undiscoverable and absent
on Android. Category: ux dead-end per the task's explicit concern list.

### 3. `manga-reader.tsx` same blank-screen-on-missing-params pattern, lower severity
`app/(app)/(media)/manga-reader.tsx:9-15`. `mediaId`/`provider`/`chapterId` come from
`useLocalSearchParams`; if any is missing/malformed, `return null` with no fallback UI. Lower
severity than #2 because nothing in `(media)/_layout.tsx` disables swipe-back for this route
(only `player` has `gestureEnabled: false`), so the iOS swipe-back gesture remains an
(undiscoverable but present) escape hatch. Still worth fixing: render a minimal "couldn't open
this chapter, go back" state instead of silent blank.

### 4. `player.tsx` `handleBack` has no fallback when there's no back stack (inconsistent with the file's own `closePlayerToEntry`)
`app/(app)/(media)/player.tsx:617-629` (`handleBack`, used by the `ControlsOverlay` back button
and the error screen's "Go Back" button) calls `player.stop()` then `if (canGoBack()) back()` —
if `canGoBack()` is false, nothing else happens; the user is stuck on the player screen (which
has `gestureEnabled: false` at the layout level, so no swipe-back either — this is the only
`(media)` screen with swipe-back disabled, making the missing fallback here the most consequential
instance of the pattern). The same file's `closePlayerToEntry` (lines 643-654) *does* have a
fallback: if `canGoBack()` is false it `replace()`s to the entry screen instead. This shows the
correct pattern is already known in-file but wasn't applied to `handleBack`. All 5 current
`router.push` call sites into the player guarantee a back stack today (confirmed via grep), so
this is not yet hit in normal navigation — it's a latent trap for any future/edge entry into the
player (e.g., a deep link, once #1 above is fixed and deep links actually start landing on
arbitrary routes) or any navigation-stack manipulation elsewhere in the app.

## Near-misses considered and rejected / downgraded

- **`entry/anime/[id]/index.tsx` "Go back" button** (`router.back()` with no `canGoBack()`
  guard, offline-unavailable state). Same shape as the player.tsx issue, but every current
  navigation into this route is a `push` from library/discover/etc., and — per finding #1 — the
  `ServerUrlWrapper` bug means a cold-launch deep link into this route today gets redirected to
  the library tab *before* the user could ever see this "Go back" button anyway. Not raised as a
  separate top-level finding since finding #1 already explains why it's not independently
  reachable today; folded into the finding-#1/#4 narrative rather than double-counted.
- **`entry/manga/[id]/index.tsx`** validates `initialView` against `VALID_VIEWS` but does not
  itself destructure/validate `id` from params. Checked `MangaEntryScreen` (the child component
  it renders) and confirmed it independently reads and validates `id` via its own
  `useLocalSearchParams` — the route file delegating that validation to the screen component is
  consistent with how `entry/anime/[id]/index.tsx` is structured too, so this is not a gap.
- **`player.tsx` line ~975** — `if (loadingMessage && !source)` shows a spinner, but if
  `source`, `loadingMessage`, and `error` are all falsy the code falls through to render the
  full player UI with an undefined `player.videoSource`. Checked
  `src/components/features/player/player-controls.tsx`: `ControlsOverlay`'s back button renders
  regardless of `source` (gated only on `controlsVisible && !controlsLocked`), so this state is
  recoverable by tapping to reveal controls and using the (also-flawed, see finding #4) back
  button. Not raised as an independent finding — folded as context into finding #4's rationale,
  not double-counted as its own bug since a concrete trigger for `source`/`loadingMessage`/`error`
  all being falsy simultaneously during normal use wasn't established.
- **All other `return null` / `router.back()` hits swept via grep** — traced each one; all
  outside the 4 findings above are either (a) sub-component render guards with no route-level
  reachability implication (e.g., `DiscoverSectionSkeleton`/`FooterLoader`/`ActiveStreamBadge`-
  style helpers returning `null` for an empty list/non-loading state), or (b) `router.back()`
  calls on screens only ever reached via `push` from a menu within the same session (e.g.
  `(profile)/my-lists.tsx`, `(media)/anime-downloads.tsx`, `discover/search.tsx`), where a
  missing back-stack is not a realistically reachable state.
- **`(app)/_layout.tsx` background-service mounts** (`BackgroundServices`, `PlayerEventMount`
  mounted unconditionally as siblings of the `Stack`) — noted as "all screens under `(app)`
  always carry this weight" but this is evidently intentional (offline sync, download-queue
  resume, watch-room live state, ws latency probe all need to run app-wide) and not scoped
  tighter by accident; not flagged as a defect.
- **`(media)/_layout.tsx`** `gestureEnabled: false` specifically on `player` — intentional
  per an inline devnote ("disabling swipe-back on the player to prevent accidental exits"); not
  a bug, but it is the reason finding #4's missing fallback is more consequential for `player.tsx`
  than the equivalent gap would be elsewhere.
- Config files (`app.config.ts`, `index.js`, `metro.config.js`, `babel.config.js`) — no sanity
  issues. Version hygiene consistent (`0.1.24` / iOS build `24` / Android versionCode `24`).
  `LSApplicationQueriesSchemes` list present for external players. EAS `checkAutomatically:
  "NEVER"` + pinned `expo-channel-name: stable` header — deliberate per inline comment (local
  sideloaded IPAs need the channel baked in since they don't go through `eas build`).
- `app/(out)/set-server-url.tsx` — reasonable error-message branching, connectivity check before
  commit. Commented-out dead code (a pre-fill effect, a "run server on this phone" button) is
  clearly intentional scaffolding, not a defect — not flagged.
- Library/manga library screens, download-queue screens (anime/manga × queue/downloaded, 4
  files), discover/search/schedule screens, and all 8 `(profile)` screens — read in full, no
  route-level defects found. Good patterns worth noting (not findings): whitelisted-set param
  validation in `entry/anime/[id]/index.tsx` and `discover/search.tsx` (`type` param checked
  against `"anime"|"manga"` before use); `useDiscoverSectionActivation` gating queries behind
  FlatList viewability to avoid firing 7+ queries on mount; tuned FlatList virtualization props
  throughout.

## Coverage

Every file in the assigned scope was read in full (route files) or via symbol-overview +
targeted full reads for the route-relevant sections of the one outsized file (`player.tsx`,
1204 lines — its dense gesture/control-overlay component body is out of this sweep's route-level
scope). No file in scope was skipped. The `tenji-audit.md` ledger was read for context to avoid
re-reporting already-fixed items; nothing in scope overlapped with an already-fixed ledger item.
