# Tenji audit — websocket layer sweep

Scope: event hub (src/api/client, src/lib) + all event-subscription call sites repo-wide.
Focus: reconnect + client-identity divergence (1038cd2), foreground fast-reconnect (402b20d),
event coverage vs server event names, parse errors, listener leaks on unmount, out-of-order/
dropped events during reconnect.

## Files read (full)

- src/api/components/websocket-provider.tsx (the connection owner: connect/reconnect/backoff/
  identity-reconnect/foreground-reconnect)
- src/api/components/websocket-event-router.ts (generic type -> query-invalidation router)
- src/atoms/websocket.atoms.ts (parse-once message hub: addWsMessageHandler/dispatchWsMessage/
  useWsMessageListener)
- src/api/client/client-identity.ts (clientId/proof storage + onClientIdentityChange pub/sub)
- src/lib/nakama/watch-room.ts (room event constants + useWatchRoomLiveState/useWatchRoomFollow/
  useWsLatencyProbe/useRoomWsSender/useRoomWsListener alias)
- src/lib/nakama/use-watch-room-sync.ts (player-side room sync consuming ROOM_PLAYBACK_SYNC)
- src/lib/nakama/ws-latency.ts (RTT smoothing, consumed by use-watch-room-sync.ts)
- src/lib/player/session.ts (usePlayerEventListener — torrentstream-state / debrid-stream-state /
  external-player-open-url consumer, single addWsMessageHandler subscription)
- src/lib/player/debrid-reconnect.ts (useDebridReconnectResume, consumes websocketConnectedAtom)
- src/lib/player/playback-coordinator.ts (read for completeness; no WS code, skip)
- app/_layout.tsx, app/(app)/_layout.tsx (mount sites — confirmed single WebsocketProvider mount
  at root, BackgroundServices mounts useWatchRoomLiveState/useWatchRoomFollow/useWsLatencyProbe
  once app-wide, PlayerEventMount mounts usePlayerEventListener once app-wide)
- src/components/features/nakama/watch-rooms-sheet.tsx (grep only — one more useRoomWsListener
  call site, ROOMS_UPDATED -> refetch(), unremarkable)
- Cross-checked server event names: H:/Projects/seanime/internal/events/events.go (full const
  block) and internal/handlers/websocket.go (ping/pong handling, ~line 95-130)

## Findings

### FINDING 1 (high, bug) — reconnect after a dependency change (login/logout/server-url/
protocol/auth-token) can permanently miss reconnecting because the guard checks for
`readyState === CLOSED` but the previous effect's cleanup only just called `.close()`, which
sets `readyState` to `CLOSING` (2) synchronously, not `CLOSED` (3).

File: src/api/components/websocket-provider.tsx, lines 123-125 (guard) and 161-173 (cleanup).

```ts
if (!socket || socket.readyState === WebSocket.CLOSED) {
    connectWebSocket()
}
...
return () => {
    cancelled = true
    unsubIdentity()
    appStateSub.remove()
    if (retryTimeout.current) {
        clearTimeout(retryTimeout.current)
    }
    if (currentSocket) {
        currentSocket.close()
    } else if (socket) {
        socket.close()
    }
}
```

The effect's dependency array is
`[manualOffline, serverAuthToken, sessionToken, serverUrl, serverUrlProtocol, ...]`. Any of
these changing at runtime (sessionToken flips on login/logout — `sessionTokenAtom` is an
`atomWithStorage`, see src/atoms/server.atoms.ts line 51; serverAuthToken/serverUrl/protocol
change when the user edits server settings) makes React tear down the old effect and run the
new one in the same commit:

1. Render captures `socket` = the WebSocket object currently in `websocketAtom` (unchanged
   reference — nothing nulls it on this transition).
2. Commit phase: old effect's cleanup runs first. `currentSocket.close()` (or `socket.close()`)
   is invoked. Per the WebSocket spec (and RN's WebSocket implementation), calling `close()`
   sets `readyState` to `CLOSING` (2) **synchronously**; it only becomes `CLOSED` (3) after the
   asynchronous close handshake completes.
3. Because the OLD effect's `cancelled` flag was just set `true` in that same cleanup, the OLD
   effect's own `"close"` event listener (registered inside its own `connectWebSocket()`) will
   later fire but no-ops (`if (cancelled) return`) — so no reconnect is scheduled from that side
   either.
4. The NEW effect body runs immediately after, in the same commit. It reads the SAME `socket`
   reference (still `readyState === CLOSING`, not yet 3). The guard
   `!socket || socket.readyState === WebSocket.CLOSED` evaluates to `false`, so
   `connectWebSocket()` is skipped entirely.
5. Nothing else in the new effect calls `connectWebSocket()` — the identity-change listener and
   the `AppState` "active" listener are reconnect *triggers*, not periodic checks; neither fires
   from a plain dependency change. The websocket is now dead until the user backgrounds and
   foregrounds the app (AppState listener) or an identity-divergence event happens to fire.
6. Because the old effect's `close` handler no-op'd (step 3), `websocketConnectedAtom` /
   `websocketConnectionStateAtom` are never updated to reflect the drop — they keep whatever
   value they had before the transition (typically `true` / `"connected"`), so the app has no
   signal that the socket is actually dead.

Concrete failure scenario: app is running with a server configured, user is logged out
(`sessionToken` empty). WS is connected. User logs in on the login screen (`sessionTokenAtom`
flips from `null`/empty to a session token). The provider's effect tears down and re-creates,
but per the race above, `connectWebSocket()` is skipped, `websocketAtom` keeps referencing the
now-CLOSING-then-CLOSED-in-the-background socket, and `websocketConnectedAtom` stays `true`.
From this point: no toast/progress/settings-changed/extensions-reloaded/nakama events reach the
client; `useWatchRoomFollow`'s room state goes stale; and — compounding this —
`useDebridReconnectResume` (src/lib/player/debrid-reconnect.ts) reads exactly this
`websocketConnectedAtom` to decide whether to arm/fire the debrid mid-stream resume-after-
reconnect logic. Since the atom never flips to `false`, `droppedWhileActiveRef` is never armed,
so if a debrid stream was active around the same time as this transition, the designed recovery
path silently never fires either. Recovery only happens if the user backgrounds/foregrounds the
app (forces `connectWebSocket()` unconditionally after clearing backoff) — until then the app
looks connected but receives nothing.

This directly falls in the "reconnect + client-identity divergence logic" hot path named in the
task (the same file/effect as the 1038cd2 identity-divergence fix), and is a plain reachable
regression risk any time that effect's dependency set changes while the app stays foregrounded.

Suggested fix direction (not applied — audit only): check `readyState !== WebSocket.OPEN &&
readyState !== WebSocket.CONNECTING` instead of `=== CLOSED`, or better, don't rely on the
stale `socket` closure at all — unconditionally call `connectWebSocket()` at the start of every
effect run (the function itself already no-ops via `cancelled`/is idempotent per invocation),
or explicitly null `websocketAtom` in the cleanup so the new effect's `!socket` branch takes it.

### FINDING 2 (low, smell) — silent JSON parse / malformed-message swallow with no diagnostics

File: src/api/components/websocket-provider.tsx, lines 98-117.

```ts
s.addEventListener("message", event => {
    if (typeof event.data !== "string") {
        return
    }
    try {
        const message = JSON.parse(event.data) as { type?: string; payload?: unknown }
        if (typeof message?.type !== "string") return
        ...
        dispatchWsMessage({ type: message.type, payload: message.payload })
    }
    catch {
    }
})
```

Any malformed frame (parse failure) or a frame missing/mistyping `type` is dropped with zero
trace. Given the codebase already invested in `NAKAMA_ROOM_EVENTS.ROOM_DEBUG` specifically
because "iOS has no devtools" (see src/lib/nakama/watch-room.ts comment), the same blind spot
applies here — a malformed/unexpected server frame during a live debugging session is
invisible. Not a functional bug (no evidence any real server frame fails to parse — cross-
checked internal/events/events.go, all constants used by the client match server names
verbatim: client-identity, external-player-open-url, torrentstream-state, debrid-stream-state,
nakama-room-reconnected, nakama-watch-room-state/closed, nakama-room-playback-sync/status,
ping/pong (handled server-side in internal/handlers/websocket.go lines 109-124), etc.) — so
capped at low/smell per the evidence-discipline rule (no concrete failure scenario, just an
observability gap).

## Rejected / near-miss leads (investigated, not bugs)

- **useWsMessageListener possible listener leak**: checked `src/atoms/websocket.atoms.ts`
  lines 32-38. `React.useEffect(() => addWsMessageHandler(...), [type])` correctly returns the
  unsubscribe function as the effect's own cleanup; `cb.current = onMessage` runs every render
  outside the effect so the closure always calls the latest callback without re-subscribing.
  No leak.
- **dispatchWsMessage mutation-during-iteration**: iterates `[...wsMessageHandlers]` (a copied
  array), so a handler that unsubscribes itself mid-dispatch can't skip a sibling handler. Fine.
- **onClientIdentityChange re-entrancy on the socket's own client-identity push**: in
  websocket-provider.tsx's message handler, `socketIdentityId.current = payload.clientId` is
  set *before* `saveClientIdentityFromEvent(...)` triggers `identityListeners`. So when the
  socket's own identity-listener callback runs synchronously inside that same dispatch, the
  early-return guard (`identity.clientId === socketIdentityId.current`) is already true and it
  no-ops. No self-triggered reconnect loop on a socket's own client-identity ack.
  (client-identity.ts lines 92-122, websocket-provider.tsx lines 106-113, 133-145)
  Also confirmed `identityListeners` iterated as a live `Set` (client-identity.ts line 116);
  since only the currently-iterated element could plausibly self-delete and JS Set iteration
  is defined-safe for that case, no correctness issue.
  Also confirmed `identityListeners` iteration order doesn't currently allow a listener to
  unsubscribe more than the JS spec already tolerates — no bug found here.
- **useWatchRoomLiveState / useWatchRoomFollow / useWsLatencyProbe "never called" scare**: an
  initial `grep -rln` scoped to `src/` only turned up references inside their own defining
  files (watch-room.ts, use-watch-room-sync.ts comments, ws-latency.ts comment). This looked
  like a dead/half-wired app-wide hook at first — but the actual mount site lives outside
  `src/`, in `app/(app)/_layout.tsx`'s `BackgroundServices` component (all three are mounted
  there, plus `usePlayerEventListener` in a separate `PlayerEventMount`). False alarm — these
  ARE mounted exactly once, app-wide, as their comments claim. (Scope note for future sweeps:
  the router tree lives in `app/`, not `src/app/`.)
- **useWatchRoomFollow's socket-rejoin effect** (watch-room.ts lines 196-204): dedupes by
  `rejoinedSocketRef.current === socket` reference identity; a genuine reconnect produces a new
  WebSocket object so the ref check correctly re-fires the rejoin exactly once per new socket.
  No double-join, no leak.
- **Identity-driven reconnect backoff delay**: closing the socket from the identity-change
  listener routes through the same effect's own `close` handler, which schedules a reconnect
  via the exponential backoff timer (starting ~1500ms) rather than reconnecting immediately.
  This is a minor latency (not a correctness bug — comment explicitly says "close handler
  schedules the reconnect") and doesn't reset `retryCount`, so if the socket had already backed
  off before the identity change, the identity-triggered reconnect could wait up to the current
  backoff ceiling (30s). Considered for a finding but downgraded/dropped: no concrete evidence
  this backoff state is commonly elevated at the moment of an identity divergence (which
  typically happens right after a fresh connect, i.e., low retryCount), and the effect is
  explicitly gated to at most once per 10s already so it's not a runaway. Not reported.
- **ping/pong latency probe correctness**: cross-checked against
  internal/handlers/websocket.go — server handles `type: "ping"` and echoes
  `{type:"pong", payload:{timestamp}}` to the same connection id. Client's
  `useWsLatencyProbe` sends `{timestamp: Date.now()}` every 5s and computes RTT from the pong.
  Matches; `recordRtt` clamps to `0 <= rttMs <= 5000` and EMA-smooths. No bug found.
- **Event name coverage**: diffed every WEBSOCKET_EVENTS constant in
  websocket-event-router.ts, NAKAMA_ROOM_EVENTS in watch-room.ts, and the string literals used
  in session.ts's handleMessage against internal/events/events.go's full const block. All
  client-side names used are exact string matches to server constants. Server emits several
  events Tenji's router doesn't handle (e.g. `auto-scan-started`, `scan-progress`,
  `scan-status`, `console-log`/`console-warn`, `show/hide-indefinite-loader`,
  `active-torrent-count-updated`, most `nakama-*` legacy/watch-party events, playlist events) —
  these fall through the router's `default: return` silently. Per the audit's exclusion (b)
  ("feature-parity gaps... only report tenji code that exists and is broken") these are not
  reported as findings; unhandled event types are not inherently a bug (default case is a
  deliberate no-op), and there's no evidence any UI/state depends on them being handled.
- **usePlayerEventListener's large dependency array** (session.ts lines 748-749): could in
  theory cause the effect (and its `addWsMessageHandler` subscription) to tear down/re-mount
  more often than necessary if any of the many setter identities were unstable, momentarily
  dropping WS messages between unsubscribe and resubscribe. Checked: all are jotai `useAtom`
  setters (referentially stable across renders by design) plus `router` (expo-router,
  stable per navigation state) and `serverUrl` (only changes on actual server switch). Not
  reported — no evidence of unstable identities causing churn in practice.

## Coverage

Read every file matched by the initial scope search (`*event*`, `*ws*`, `*socket*` excluding
generated, plus grep for all `useWsMessageListener`/`addWsMessageHandler`/`useRoomWsListener`
call sites repo-wide) in full, not sampled. Also read the two `_layout.tsx` mount points to
confirm hook mount cardinality, and cross-referenced every client-side WS event-name string
against the server's `internal/events/events.go` and the `ping`/`pong` handling in
`internal/handlers/websocket.go`. Did not deep-dive the nakama room *player sync* math itself
(drift/nudge/heartbeat logic in use-watch-room-sync.ts) beyond confirming its WS subscription
pattern is sound — that domain (playback sync correctness) is more player-audit territory than
websocket-layer territory and is largely already covered by extensive in-code comments
describing past bugs and their fixes (Issue-A echo bug, buffering-hold, etc.), so no further
audit obligation was in scope here. Did not audit `src/api/generated/*` per exclusion (e).
