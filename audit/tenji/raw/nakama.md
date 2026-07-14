# Tenji audit — scope: src/components/features/nakama/ + src/lib/nakama/

Agent: nakama sweep. Date: 2026-07-10.

## Files read (all in scope, exhaustive)

- src/lib/nakama/watch-room.ts (242 lines)
- src/lib/nakama/use-watch-room-sync.ts (294 lines)
- src/lib/nakama/ws-latency.ts (23 lines)
- src/components/features/nakama/room-stream-join-fab.tsx (22 lines)
- src/components/features/nakama/watch-rooms-sheet.tsx (279 lines)

Cross-checked against server (H:/Projects/seanime, read-only, for endpoint/event semantics only):
- internal/nakama/watch_room.go (JoinRoom, LeaveRoom, RelayPlaybackStatus, control-handoff logic)
- Confirmed ledger H:/Projects/seanime/tenji-audit.md §0: T1–T13 all fixed, none of them touch this scope
  except T6 (nakama-room-reconnected handler, T8 (single JSON.parse) — both already landed and present
  in the code I read (ROOM_RECONNECTED handler exists at watch-room.ts:185-190; useWsMessageListener is
  the shared parse-once hub).

Also read (for context, not part of scope, not separately reported):
- src/atoms/websocket.atoms.ts (useWsMessageListener / addWsMessageHandler — confirmed proper
  subscribe/unsubscribe via useEffect cleanup, and that the callback ref is refreshed every render so
  there's no stale-closure risk for any `useRoomWsListener` callback in this scope)
- src/api/hooks/nakama.hooks.ts (useNakamaJoinWatchRoom etc. — thin useServerMutation wrappers, no
  extra client logic worth auditing separately)

## Findings kept (see StructuredOutput)

1. **Stale WATCH_ROOM_STATE can resurrect a room after the user pressed Leave** (medium, bug)
   - watch-room.ts:79-89 `useWatchRoomLiveState` accepts ANY WATCH_ROOM_STATE push as long as
     `amMember` (this clientId appears in the pushed snapshot's participants) is true. It has no check
     against "did I just leave" / no epoch or generation guard.
   - watch-rooms-sheet.tsx:178-186 `Leave` button clears local room state optimistically in
     `onSuccess`/`onError` of the HTTP leave mutation (`setOptedOut(null); onLeft()` → `setCurrentRoom(null)`).
   - HTTP leave (POST) and the room-state broadcast (WS) are different channels with no ordering
     guarantee. A WATCH_ROOM_STATE broadcast triggered by ANOTHER member's concurrent action (control
     toggle, autoskip vote, heartbeat-adjacent broadcast) that was already in flight from the server
     BEFORE it processed this client's leave still contains this client in `participants`. If that frame
     arrives after the local leave has cleared the atom, `setRoom(room)` fires and repopulates
     `currentWatchRoomAtom` with the stale (already-invalid server-side) snapshot.
   - User-visible effect: user taps Leave, sheet correctly flips to DiscoveryPanel for a moment, then
     seemingly "un-leaves" back to InRoomPanel. Any further action from that screen (control toggle,
     autoskip vote) will hit a server that no longer has them as a participant, so the mutation likely
     no-ops/fails silently, and the UI stays wedged showing a room they aren't in — a dead-end until they
     reopen the sheet and see the real state (which requires no further stray broadcasts, or force-close/
     reopen).
   - Confirmed via server source (internal/nakama/watch_room.go) that `broadcastRoomState` sends the
     participant snapshot at broadcast time, and JoinRoom emits a fresh broadcast; LeaveRoom does not
     broadcast a room-state to the leaver but does when the host leaves. Nothing coalesces intents, so
     the race is real, just narrow (needs another member's action landing in the same short window as
     the leave call).

2. **`optedOutStreamRoomIdAtom` opt-out is scoped to the room, not the stream — permanently disables
   auto-follow for the room after one mid-stream join** (medium, bug)
   - watch-room.ts:44 declares the atom as "the roomId whose active stream this client has opted OUT
     of." It is only ever set in watch-rooms-sheet.tsx:64
     `setOptedOut(r.playbackActive ? r.id : null)` (join a room while a stream is already live), and
     only ever cleared in two places: leave (watch-rooms-sheet.tsx:182-183) and explicit
     "Join room stream" click (watch-room.ts:229, inside `useRoomStreamJoin.join`).
   - `maybeFollow` (watch-room.ts:128-164) gates purely on `optedOutRoomId === p.roomId` (line 143) —
     it does not compare against the specific mediaId/episodeNumber that was live when the opt-out was
     recorded, and nothing resets it when that original stream ends (`p.stopped` branch, lines 133-138,
     touches only `followedKeyRef`, not the opt-out atom).
   - Concrete scenario: User B joins Room X while Episode 1 is already playing (`playbackActive=true`)
     → opted out of Room X. User B does not press "Join room stream" for ep1. The host finishes ep1 (stop
     broadcast) and starts Episode 2 in the *same* room. User B's `maybeFollow` still sees
     `optedOutRoomId === p.roomId` (unchanged, still Room X) and returns early — ep2 never auto-opens for
     User B, even though nothing about ep2 should be "declined." User B must notice and tap the
     "Join room stream" FAB every single future episode in that room for the rest of the session, unlike
     someone who joined the room before any stream started (who auto-follows every episode).
   - Not a hard dead-end (the FAB does correctly compute `canJoin` off `room.playbackActive` each time,
     per watch-room.ts:219-225), but it silently and permanently changes this room's behavior from
     auto-follow to manual-only after the first mid-stream join, which isn't the stated per-stream intent
     and isn't surfaced to the user anywhere.

3. **Redundant re-join fired on every first room-join, not only on reconnect** (low, perf/smell)
   - watch-room.ts:196-204, the "reconnect" rejoin effect:
     ```ts
     const rejoinedSocketRef = React.useRef<WebSocket | null>(null)
     React.useEffect(() => {
         if (!socket || rejoinedSocketRef.current === socket || !room) return
         rejoinedSocketRef.current = socket
         joinRoom({ roomId: room.id, password: "", clientId }, { ... })
     }, [socket, room?.id])
     ```
   - Trace: on cold mount, `room` is null, so the guard `!room` always short-circuits and the ref is
     never populated while the socket is (potentially) already open. The first time `room` becomes
     non-null is precisely when a user finishes joining a room via `DiscoveryPanel` (watch-rooms-sheet.tsx:58-68),
     which already performed the join over HTTP and set the atom from the response. Because
     `rejoinedSocketRef.current` was never set for the pre-join socket, this effect's guard passes on
     that very first room-join render and fires a second, redundant `joinRoom` call against the same
     room/clientId immediately after the real one succeeded.
   - Server-side (internal/nakama/watch_room.go:354-368) the second call hits the "already a member"
     branch: harmless no-op field reset for a non-host joiner, but it still triggers a fresh
     `broadcastRoomState` to every participant in the room — wasted traffic and an extra render for
     everyone in the room, proportional to how many people join. Not incorrect, just not what the
     "only re-join after a reconnect" comment describes; the guard should also arm on the socket that
     was current when the join actually completed (e.g. seed `rejoinedSocketRef` from the join's
     onSuccess, or key off `room.id` transitions from null only when they don't correspond to a fresh
     local join).

## Rejected / near-misses (traced, not reported)

- Considered: `ROOM_PLAYBACK_SYNC` listener in `useWatchRoomFollow` has no explicit `room` /
  `p.roomId === room.id` gate before processing (only `driverGuard`, opt-out, and dedupe-by-key checks).
  Traced server-side: `RelayPlaybackStatus` (internal/nakama/watch_room.go:583+) uses
  `wsEventManager.SendEventTo(cid, ...)` targeted at the current room's participant client IDs only, not
  a broadcast — a client not (or no longer) in the room won't normally receive this event. Only a very
  tight timing race (a driver's already-scheduled tick landing right as this client's leave is applied
  server-side) could deliver a stray one, and worst case it just re-opens the same media the user was
  legitimately just watching in that room — much narrower and lower-impact than finding #1, so not
  reported separately.
- Considered: `emitNow`/`useEffect` deps include `state.currentTime`, so `emitNow` (and thus the
  paused-toggle effect that lists it as a dep) is a new function identity almost every tick. Traced:
  the paused-toggle effect early-returns unless `state.paused` itself flipped, so this just means the
  effect body re-runs (and immediately exits) more often than logically necessary — no incorrect
  behavior, negligible cost. Not reported.
- Considered: non-host controllers ("everyone can control") emitting local actions gated only on
  `canControl`, not `amController` (use-watch-room-sync.ts:106). Traced server-side control-handoff
  logic (internal/nakama/watch_room.go:655-672): this is the intended "shared remote" model — a
  discrete action from any controlling, non-driving member is meant to hand control to them
  server-side. Working as designed, not reported.
- Considered: closing a non-host controller's player leaves the room "frozen" (no more heartbeats,
  controllerKey not reclaimed). This is explicitly called out and mitigated in existing comments
  (use-watch-room-sync.ts driver-exclusion note, watch-room.ts:220-224 canJoin comment) — a known,
  already-addressed tradeoff, not a new finding.
- Considered: `WatchRoomsSheet`'s `DiscoveryPanel`/`InRoomPanel` don't rehydrate `currentWatchRoomAtom`
  from the server after a cold app restart (atom just starts null, no "am I still in a room?" query).
  This is a feature/parity-shaped gap (whether web/Denshi do this either wasn't checked, and the
  exclusions list rules out parity gaps), not a broken-code defect in this scope, so not reported.
- Considered: hardcoded FAB position (`bottom: 96, right: 16`, no safe-area-aware inset) in
  room-stream-join-fab.tsx:14. Plausible but unverified without seeing how/where the FAB is mounted
  relative to any tab bar; too speculative to cite a concrete failure scenario, dropped per evidence
  discipline (would need to be capped at low/smell anyway, and I have no repro).
- `everyoneControls = entries.every(...)` on an empty `entries` array evaluating true (watch-rooms-sheet.tsx:169)
  is technically vacuous-truth, but a room with a currentWatchRoomAtom set and zero participants is not
  a reachable server state (rooms are deleted when they empty — internal/nakama/watch_room.go:432-444),
  so not reported.

## Coverage note

All 5 files in scope were read in full, not sampled. Cross-referenced the relevant server-side handlers
(JoinRoom, LeaveRoom, RelayPlaybackStatus with its control-handoff/echo-rejection logic) to determine
which client-side gaps are actually reachable given server-side scoping/targeting, rather than reporting
speculative races the server design already forecloses.
