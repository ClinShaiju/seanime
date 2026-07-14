# Tenji audit — scope: src/api/client/, src/api/loaders/, src/api/components/

Date: 2026-07-10. Repo: H:/Projects/seanime-tenji. Ledger checked first: H:/Projects/seanime/tenji-audit.md §0 (T1-T13 all fixed as of v0.1.22) — no re-reports of those below.

## Files read (all files in scope, full read)

- src/api/client/client-identity.ts
- src/api/client/requests.ts
- src/api/client/server-auth.ts
- src/api/client/server-url.ts
- src/api/components/api-loaders.tsx
- src/api/components/server-data-wrapper.tsx
- src/api/components/websocket-event-router.ts
- src/api/components/websocket-provider.tsx
- src/api/loaders/collection.loaders.ts

Supporting context read (outside scope, to verify call sites / cross-check server semantics — not audited themselves):
- src/api/hooks/report.hooks.ts (call sites of buildSeaQuery-backed hooks)
- src/lib/utils/toast.ts (toast lib behavior — no de-dup)
- src/lib/connection-state.ts (markServerReachable/Unreachable, isGlobalConnected semantics)
- src/lib/offline/download-snapshot-refresh-service.ts (another buildSeaQuery direct caller — small JSON payloads only, timeout is fine there)
- H:/Projects/seanime/internal/handlers/report.go, internal/handlers/server_auth_middleware.go, internal/util/hmac_auth.go (server-side cross-check only)

## Findings (see StructuredOutput for the formal list; details/evidence here)

### F1 [HIGH, bug] Every mutation and most queries surface a doubled, inconsistent error toast

`buildSeaQuery` (requests.ts:158-181) already normalizes the error and shows its own toast in its `catch` block:
```
if (!wasAborted && !muteError && isGlobalConnected()) {
    toast.error("An error occurred: " + seaError.error, { visibilityTime: 5000 })
}
...
return Promise.reject(seaError)
```
The rejection then propagates to the caller, which does its OWN, separate toast:
- `useServerMutation`'s `onError` (requests.ts:200-214): `toast.error(_handleSeaError(error.error))` whenever `isGlobalConnected()`, with NO way to opt out — `ServerMutationProps` (requests.ts:183-186) has no `muteError` field at all, and `mutationFn` (216-223) never passes one to `buildSeaQuery`. So `muteError` is always `undefined` for every mutation in the app → buildSeaQuery's own toast always fires too. Net result: **every failed mutation (except UNAUTHENTICATED) shows two toasts** — "An error occurred: X" immediately followed by "Server Error: X" (from `_handleSeaError`, requests.ts:285-306, which for a plain string just returns `"Server Error: " + data`).
- `useServerQuery`'s `useEffect` (requests.ts:266-278) does the same second toast for any query where the caller didn't pass `muteError: true`. Only 5 of the many `useServerQuery` call sites set `muteError` (directory_selector, manga, onlinestream×2, status — grep across src/api/hooks). Every other query double-toasts on failure the same way.
- Neither outer handler checks `wasAborted` — so a **request that times out (the 45s `REQUEST_TIMEOUT_MS`, requests.ts:22) always produces the outer toast** ("Server Error: Aborted" / whatever the AbortError message is), even though `buildSeaQuery` itself deliberately suppresses its own toast for aborts (`!wasAborted` at requests.ts:170). This directly relates to the "was the 45s timeout applied correctly everywhere" question from the task brief: the timeout mechanism is applied uniformly, but the two layers of error-toast logic built around it disagree about whether a timeout should be user-visible, and the visible one is the unfiltered layer.

Failure scenario: any `useServerMutation` call (e.g. `useDeleteLogs`, status.hooks.ts) fails for any reason online — user sees "An error occurred: <msg>" then near-instantly "Server Error: <msg>". Toast lib (src/lib/utils/toast.ts) has no de-dup/queueing beyond `react-native-toast-message`'s default single-instance replace-on-show, so the two calls either flash-replace each other or (depending on RN's microtask scheduling between the `buildSeaQuery` catch and React Query's `onError` callback) can render as a visible double-toast. Either way it's dead, duplicated code paths that both fire in the common case — not a display nuance, a logic bug in requests.ts.

Rejected as separate finding: I considered filing "abort never shown" and "double toast" as two bugs, but they're two symptoms of the same root cause (buildSeaQuery does its own full error-surfacing AND every caller redundantly does its own on top, inconsistently) so I combined them into one finding citing all three code spans.

### F2 [MEDIUM, bug] buildSeaQuery hard-codes JSON in both directions — breaks the app's only FormData upload and only binary download

requests.ts:101-112:
```
const options: RequestInit = {
    method,
    headers: { "Content-Type": "application/json", ...getServerAuthHeaders(...), ...getClientHeaders() },
}
if (data && method !== "GET") {
    options.body = JSON.stringify(data)
}
```
and requests.ts:124-149 unconditionally does `await response.text()` then `JSON.parse(text)`, falling back to returning the raw text if parsing fails.

Two concrete call sites exercise the two directions this breaks (src/api/hooks/report.hooks.ts):
- `useDecompressIssueReport` (report.hooks.ts:26-32) types its mutation variable as `FormData` and the server handler expects a multipart upload (`internal/handlers/report.go:144-156`, `c.FormFile("file")`). Once wired to a UI button, `JSON.stringify(new FormData())` serializes to `"{}"` (FormData has no enumerable own properties) and is sent with `Content-Type: application/json` — the server's `c.FormFile("file")` lookup fails immediately; the feature cannot work as written.
- `useDownloadIssueReport` (report.hooks.ts:17-24) types its query result as `string` and hits `GET /api/v1/report/issue/download`, which the server streams back as `Content-Type: application/zip` raw bytes (report.go:130-136), not JSON. `buildSeaQuery` calls `response.text()` on it, which UTF-8-decodes the zip's binary bytes — non-UTF8-valid byte sequences get replaced/mangled — then `JSON.parse` throws (caught) and the mangled string is returned as `T` since `response.ok`. Any caller that tries to write/share that string as a zip file gets a corrupted archive.

Both hooks are currently **dead code** (zero call sites beyond their own definition — confirmed via grep across src, and the only other reference is a commented-out template in `src/api/generated/hooks_template.ts:2344`), so nothing breaks *yet* — same situation as ledger item T1 (dead-code URL bug, rated MED, "404s the moment an admin screen uses it"). I've matched that precedent for severity: this is a structural gap in the shared fetch wrapper (in scope) that will silently break the moment either hook is wired to UI, and there is currently no supported way to do a file upload or raw-binary download through `buildSeaQuery` at all.

### F3 [MEDIUM, perf] useAnilistCollectionLoader runs three full-collection reduces inside useLayoutEffect, blocking the JS thread on every refresh

collection.loaders.ts: three separate `React.useLayoutEffect` blocks (lines 23-45, 48-60, 64-85), each doing `flatMap → filter(Boolean) → reduce` over the *entire* AniList anime collection, the *entire* local library collection, and the *entire* AniList manga collection respectively, every time `useGetAnimeCollection`/`useGetLibraryCollection`/`useGetRawAnilistMangaCollection` produce a new object reference. This hook is mounted at the top of the tree via `ApiLoaders` (api-loaders.tsx), which every gated screen renders through (`ServerDataWrapper` renders `<ApiLoaders>{children}</ApiLoaders>` in the normal case, server-data-wrapper.tsx:198-201).

`useLayoutEffect` fires synchronously before the frame is presented (blocks paint) — appropriate when you need to measure/mutate the DOM before the user sees a flash, but there's no layout read/write happening here, only three JS reduces over collections that can run to 1000+ entries for an active AniList user. Every collection refresh (websocket `refreshed-anilist-anime-collection` / `refreshed-anilist-manga-collection` events via websocket-event-router.ts, or a plain refetch/app-foreground refresh) re-triggers all three passes synchronously and will visibly jank/drop frames on lower-end devices. Using `React.useEffect` instead would let React paint first and defer the recompute by one frame, which is invisible to the user here (the derived atoms aren't needed for the very first paint of any screen) — free win, no functional need for `useLayoutEffect` semantics.

### F4 [LOW, smell] Collection reducers key by `n.media?.id!`, silently coalescing null-media entries under a literal "undefined" key

collection.loaders.ts:30 and :69: `acc[String(n.media?.id!)] = {...}`. The non-null assertion masks that `n.media` can legitimately be `null` for an AniList list entry whose linked media was delisted/hidden (a real, if uncommon, AniList data state) — `filter(Boolean)` upstream only drops falsy *entries*, not entries with a null `.media`. When it happens, `String(undefined)` = `"undefined"` and every such entry overwrites the same synthetic key with the last one's status/progress/score, silently discarding the earlier ones. No crash and no legitimate code path currently looks up the literal key `"undefined"`, so the practical impact is just quietly-wrong/lost data for whichever handful of entries have a null media — capped at low per the evidence-discipline rule since I can't point to a UI symptom, only the data being wrong in the atom.

### F5 [LOW, smell] Timeout aborts never mark the server unreachable, so a persistently-timing-out connection never settles into "offline"

requests.ts:61-76 `isConnectivityFailure` explicitly returns `false` for `AbortError` (`if (error instanceof Error && error.name === "AbortError") return false`), so `markServerUnreachable()` (connection-state.ts:107-118) is never invoked from a timeout — only from an outright `TypeError`/"network request failed"-style immediate failure. On a connection that is bad-but-not-dead (e.g. weak cellular where every request hangs until the 45s `REQUEST_TIMEOUT_MS` abort fires, but DNS/TCP still connects), `serverReachability` stays `"reachable"`/`"unknown"` forever, so `isGlobalConnected()` (connection-state.ts:124-128) stays `true` throughout. Combined with F1, this means every timed-out request keeps producing a user-visible toast every ~45s instead of the app ever recognizing degraded connectivity and switching to the quiet/offline UX path (`ServerDataWrapper`'s offline fallback, connection banners elsewhere). Partially mitigated in practice because a bad-enough connection usually also drops the websocket, which independently calls `degradeServerReachability()` on close (websocket-provider.tsx:86) — but that only moves state to `"unknown"`, which `isGlobalConnected()` still counts as connected, so it doesn't actually suppress anything either. Capped at low/smell: plausible but narrower window (needs a connection that's bad enough to always hit 45s timeouts but not bad enough to blackhole/refuse the TCP/TLS handshake outright).

## Near-misses rejected (checked, no bug)

- `getServerBaseUrl` / `devOrProd` (server-url.ts) always returns prod regardless of `NODE_ENV` — the dev branch is explicitly commented out. Clearly an intentional toggle left in a fixed state, not a bug.
- `server-auth.ts` HMAC token generation (`HMACAuth.generateToken`) — cross-checked byte-for-byte against `internal/util/hmac_auth.go`: base64url-no-padding encoding matches, JSON key order matches (`{endpoint, iat, exp}` in both the JS object literal and the Go struct's declared field order under `json.Marshal`), TTL semantics match. No bug found.
- `ServerDataWrapper`'s UNAUTHENTICATED handling (`setServerAuthToken(null)` + redirect to `/(out)/set-server-url`) looked aggressive at first (wiping the server connection entirely rather than just showing a per-user login screen) — cross-checked against `internal/handlers/server_auth_middleware.go:83`: `UNAUTHENTICATED` is emitted *only* for the shared server-password gate (`X-Seanime-Token` / `serverAuthToken`), never for the separate per-user `sessionToken`/Bearer layer, so tearing down the server connection on that specific error is correct, not a bug.
- `ServerUrlWrapper`'s redirect `useEffect` (server-data-wrapper.tsx:20-32) only depends on `[serverUrl]`, not `pathname` — looked like it could fail to re-guard after in-app navigation, but the setup screen is explicitly documented/designed to have "no back action" (see the confirm dialog text at server-data-wrapper.tsx:92-93), so this is consistent with intended behavior, not investigated further as a bug.
- `websocket-provider.tsx`'s reconnect/backoff/AppState/identity-change logic (T5/T8 fixed items) — re-verified the closure semantics (`currentSocket` mutable capture, `socketIdentityId`/`lastIdentityReconnect` refs) are consistent across reconnects within one effect instance; no stale-closure or double-socket bug found.
- POST/PATCH/PUT/DELETE `useServerQuery` calls combined with `params` (which `buildSeaQuery` silently drops for non-GET methods, requests.ts:92-98) — grepped hooks for this combination, found zero occurrences, so not currently exercised; not filed as a live bug.
- `new URL(getServerBaseUrl(serverUrl) + endpoint)` (requests.ts:88) sits outside the `try/catch`, so a null/empty `serverUrl` would throw synchronously — but since it's inside an `async function` the throw still becomes a rejected promise, and both in-scope callers (`useServerQuery`/`useServerMutation`) gate on `enabled: !!serverUrl` before firing, so this isn't reachable through the primary hooks. Direct `buildSeaQuery` callers that might race this are all outside my scope (src/lib/offline/*).

## Coverage

All 9 files in scope were read in full. No file was sampled/skipped. Findings F1 and F2 are the highest-confidence and highest-impact; F3-F5 are lower confidence/impact but included per the "read every file, be exhaustive" instruction.
