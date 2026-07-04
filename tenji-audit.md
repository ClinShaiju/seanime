# Tenji client audit — parity, performance, inconsistencies

**Date:** 2026-07-01 · **Tenji:** `H:/Projects/seanime-tenji` @ `4c72280` (v0.1.21) · **Server:** seanime 3.8.13 · **Reference client:** `seanime-web`

> **Fix pass 2026-07-01:** every open item below was implemented in tenji commits `f6dcfcd`…`f2ab8d2` (see ledger Status column). Typecheck clean; **not yet shipped** — needs a build/OTA. T7's web-side regex was subsequently fixed in seanime `0df0a641` (goes live with the next server rebuild/deploy). iOS IPA build of the tenji fixes: Actions run 28563886136 (v0.1.22, commit `6f898e4`).

Scope: hand-synced API surface vs seanime-web and the Go server, websocket event coverage, mobile performance patterns, dead code / drift. Method: set-diffs of endpoints/events, targeted reads of the request/ws/player layers, cross-checks against Go handlers as source of truth.

## 0. Findings ledger

| # | Sev | Area | Finding | Status |
|---|-----|------|---------|--------|
| T1 | **MED** | API | `USER.List` endpoint URL is wrong: `GET /api/v1/user` — server route is `GET /api/v1/user/list`. Currently dead code (zero call sites) so nothing breaks *yet*; it 404s the moment an admin screen uses it | fixed `be3fe2f` |
| T2 | **MED** | Parity | No change-password on mobile: `/api/v1/user/change-password` isn't even in `endpoints.ts`; no screen exists. Mobile-only users of the multi-user VPS can never rotate their password | fixed `be3fe2f` |
| T3 | **MED** | Parity | Per-user settings/debrid management absent: `USER.SaveSettings` / `USER.SaveDebrid` endpoints are defined but have **no hooks and no UI**. Also `Status_UserDebrid` type lacks `useServerAutoSelect` (live on server). ⚠️ If SaveDebrid is ever wired without that field, Go decodes the missing bool as `false` and silently resets the user's auto-select preference (`HandleSaveUserDebrid` writes it unconditionally) | fixed `be3fe2f` |
| T4 | **MED** | Perf | `requests.ts` `fetch()` has **no timeout / AbortSignal**. On a dying cellular connection a request hangs until the OS gives up (60s+ on iOS); React Query retry never kicks in because the request never fails. One-liner: `signal: AbortSignal.timeout(15_000)` (the error path already special-cases `AbortError`) | fixed `f6dcfcd` |
| T5 | **MED** | Perf | Websocket provider has **no AppState handling**. iOS kills the socket on background; on foreground, reconnect waits out the exponential backoff (up to 30s) instead of connecting immediately. Add an `AppState` listener: on `active`, reset `retryCount` and reconnect now | fixed `402b20d` |
| T6 | LOW-MED | WS parity | Server events with no mobile handler that plausibly matter: `settings-changed` (settings edited from web/Denshi stay stale on mobile until the 60s status poll), `server-logged-out-anilist` (stale auth state), `nakama-room-reconnected` (no follower resync after a server-side reconnect — Denshi handles this), `refreshed-manga-download-data`, `scan-progress`/`scan-status` (no scan feedback). Full delta in §2 | fixed `402b20d` (the three recommended events; scan-progress + refreshed-manga-download-data intentionally skipped — no scan UI on mobile, manga screens self-invalidate) |
| T7 | LOW-MED | Inconsistency | Franchise title-stem drift, three ways: server strips `(season\|stage)` (`franchise.go` — catches "Initial D Second Stage"); tenji + web strip only `season`. Separately, web still uses `\s(ii\|iii…)\s` where tenji correctly uses `\b` to match Go. Only bites the no-TMDB-id fallback path, but the tenji comment claims "to match Go's FranchiseTitleStem" and it doesn't. **Web is affected too** | tenji fixed `49198e6`; web fixed `0df0a641` (live after next server rebuild/deploy) |
| T8 | LOW | WS/Perf | Every websocket message is `JSON.parse`d by up to 4 independent listeners (provider identity check, event router, player `session.ts`, nakama `watch-room.ts`). Nakama room sync emits ~1/s during watch rooms. Parse once in the provider, fan out the parsed object | fixed `402b20d` |
| T9 | LOW | Perf | `MediaEntryCard` isn't memoized and subscribes to the whole `serverStatusAtom` (37 files call `useServerStatus()`). React Query structural sharing keeps the Status identity stable across the 60s poll, so no steady-state thrash — but any *real* Status change re-renders every visible card in every list. `React.memo` the card + narrow the two settings reads into derived atoms | fixed `d97f224` |
| T10 | LOW | API | Stale generated type: `MainServerTorrentStreaming` — server renamed it `builtinTorrentClient` (`feature_flags.go`). Type-only, no runtime reads | fixed `be3fe2f` |
| T11 | LOW | Parity | My-lists filter sheet has genres only; web's lists page also filters by AniList **tags** (`GetRawAnimeCollectionTags` + `tags` search variable, both absent from tenji's generated files) | fixed `10e6dfa` |
| T12 | LOW | Dead code | Full shadcn-style `@rn-primitives/*` kit installed (menubar, navigation-menu, hover-card, context-menu, table, accordion, …) where each package is imported only by its own `src/components/ui/*` wrapper, which nothing else imports. Bundle bloat; prune wrappers + deps | fixed `f2ab8d2` |
| T13 | LOW | Inconsistency | `player.tsx` re-implements the next-episode preload inline (`useDebridStartStream` + own guard refs, line ~792) instead of reusing `useDebridPrewarm` — two copies of the same enabled/dedupe logic to keep in sync | fixed `b9990ca` |
| T14 | INFO | API | Other web-only endpoints, all fine to skip on mobile: `extensions/list/anime-entry-episode-tabs`, `extensions/external/disabled`, `mediastream/local-subtitles`, `settings/path`, `torrent-client/details`, `torrentstream/batch-history/delete`, `manga …/raw/tags` | n/a |

## 1. API parity (vs server 3.8.13)

Endpoint sets: tenji 240 URLs, web 250. The hand-sync is in good shape — every gap is enumerated above (T1–T3, T11, T14) and nothing tenji calls is missing server-side.

- **T1 detail:** `routes.go` has no `GET /api/v1/user` — the group only defines `/login /logout /me /change-password /settings /debrid /list /register` and `DELETE /:id`. Tenji's hand-added `USER` block predates the final route shape. Fix the URL when (or before) building any admin screen; also missing: `ChangePassword`, `Delete`.
- **T3 detail:** the multi-user identity plane works on mobile (login/logout/me are wired and used; `UserLoginResponse {token, user}` matches the handler exactly). What's missing is the *management* layer: per-user settings overrides, per-user debrid key/toggle, change-password. Until then, a mobile-only user needs the web UI for those.
- Hook-file diffs vs web (400–600 lines in places) are **intentional divergence**, not drift: tenji layers offline mutation queues, optimistic list-entry updates, and mobile stale-times on top of the same endpoints. `nakama.hooks.ts` is byte-identical to web. `search.hooks.ts` (infinite-scroll search) is a tenji-only addition, used by discover.
- Generated `endpoint.types.ts` is missing the franchise `*_Variables` types and the `tags` search variable — cosmetic; tenji hooks build those URLs by hand.

## 2. Websocket event coverage

Tenji's model: one central router (`websocket-event-router.ts`, 18 event types → query invalidation + toasts) plus feature listeners (`session.ts`: `debrid-stream-state`, `torrentstream-state`, `external-player-open-url`; `watch-room.ts`: `nakama-watch-room-state/-closed`, `nakama-room-playback-sync/-status`, `nakama-rooms-updated`, `nakama-room-debug`).

Covered well: toasts, anime/manga collection refresh, library watcher, auto-downloader, playback progress, chapter downloads, extensions reload, local sync, generic `invalidate-queries`, debrid/torrent stream state, nakama room sync.

Server-emitted events with no tenji handler, filtered to mobile-relevant (rest are desktop/Denshi/plugin concerns — `native-player`, `videocore`, `playlist`, `extension-prompt*`, `console-*`, watch-party legacy set, etc.):

| Event | Impact on mobile |
|---|---|
| `settings-changed` | settings edited on another client stay stale until 60s poll / refocus |
| `server-logged-out-anilist` | app keeps stale AniList auth state |
| `nakama-room-reconnected` | follower doesn't resync after server reconnect |
| `nakama-room-created` / `-closed` / `nakama-error` / `nakama-status` | rooms *list* stays fresh via `nakama-rooms-updated`, but no toasts/lifecycle awareness |
| `scan-progress` / `scan-status` | router refreshes on `auto-scan-completed` only; no progress UI |
| `refreshed-manga-download-data` | manga download screens rely on their own invalidations |
| `debrid-download-progress` | not a gap — tenji polls (`useDebridGetTorrents`, gated 1.5s interval) instead of push; just inconsistent with web |

## 3. Performance

The big wins are already in place: derived-atom per-card selectors (list/library data), memoized screen-level data prep, tuned `FlatList`s (`getItemLayout`, `windowSize`, batch sizes), `expo-image` with RN-Image fallback, gated polls (1.5s torrent list only while visible; 60s status), the prewarm badge fetch-on-mount fix (`af4636a`), exponential-backoff ws reconnect capped at 30s.

Open items are T4 (fetch timeout — the highest-value one-liner in this report), T5 (AppState-aware reconnect), T8 (N× JSON.parse), T9 (card memoization). One non-issue worth recording so it isn't "fixed" later: the 60s status poll does **not** re-render subscribers each tick — React Query structural sharing keeps the `Status` object identity stable when the payload is unchanged, and the Go `Status` struct has no volatile per-request fields.

Minor: FlashList 2.0.2 is used in exactly 3 places (manga reader, carousel, media-list) while the main grids use core FlatList — both are fine, but it's two virtualization systems to tune; converge when convenient.

## 4. State of previously-flagged items

- **`fable-prewarm.md` F3 ("tenji prewarm hook is dead code") is FIXED** in the tenji tree: `useDebridPrewarm` is called at entry-screen mount (`entry/anime/[id]/index.tsx:41`) and the in-playback next-episode preload fires from `player.tsx:792` with `preload: true, prewarmMetadata: true` (parity with web's @3s trigger, per `ed4c707`/`af4636a`). Not yet shipped to devices until the next build/OTA — the server-side chaining fix covers the gap meanwhile.
- Version hygiene is clean: `package.json` 0.1.21 = `app.config.ts` version, `versionCode` 21, `MIN_SERVER_VERSION` guard (3.8.0) with a proper semver comparator and an unsupported-server screen. No hardcoded VPS URLs, 1 TODO in the whole tree, 16 `as any`/ts-ignores, `dist/` gitignored.

## 5. Recommended order

1. **T4** fetch timeout (one line, biggest UX effect on cellular).
2. **T5** AppState reconnect (small; pairs naturally with T4 in one OTA).
3. **T1+T2+T3** fix the `USER` endpoint block and add the change-password + per-user debrid/settings screens in one "account management" pass — and add `useServerAutoSelect` to `Status_UserDebrid` *before* wiring SaveDebrid (zero-value reset trap).
4. **T6** add `settings-changed` + `server-logged-out-anilist` + `nakama-room-reconnected` to the router (three cases).
5. **T7** align the stem regex to `(season|stage)` in tenji *and* web (and web's `\b` fix) — trivial, do all three sides in one commit.
6. T8/T9/T11–T13 opportunistically.
