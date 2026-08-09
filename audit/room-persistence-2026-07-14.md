# Audit nakama watch-room persistence, longevity, empty-room cleanup, and log errors that make a room unusable over time

## Nakama Watch-Room Persistence & Longevity — Synthesis Report

Synthesis of 18 findings from four probes (logs, lifecycle, persistence, cleanup). Everything below is anchored to a specific `file:line` or log line; nothing is invented. The already-landed fixes (F6 liveness-filtered promotion, F12 `session.Participants` guard, 2min reaper, 6s `playbackStaleTTL`, per-instance opt-out, auto-follow re-arm) are treated as given.

### 1. Executive summary — what actually makes a room unusable over time / across restarts

- **Every server restart silently nukes all rooms, and neither side recovers.** `WatchRoomHub.rooms` is in-memory with no persist/rehydrate (`watch_room.go:236-246`, hub rebuilt at `nakama.go:256`, no DB model in `models.go`). The Pi auto-updates from `releases/latest` every ~15min (CLAUDE.md), so any live party is killed on the next redeploy. The client's reconnect re-join has **no `onError`** (`nakama-manager.tsx:636-645`) and `currentWatchRoomAtom` is a plain atom that survives ws reconnect — so both members sit on a frozen room panel whose controls silently drop (`RelayPlaybackStatus` returns early at `watch_room.go:615-618`). Observed live: 8 restarts in ~1.5h, each followed by `status=500 /watch-room/join` and `/control` spam (`nakama-4h.log:520-526, :817`).

- **A host closing their tab (not clicking Leave) permanently freezes the room.** `HandleClientDisconnect` promotes `ControllerKey` to the earliest live member but **never grants `CanControl`** (`watch_room.go:491-497`, `nextControllerKeyLocked:970-998`). `resolveRelay` then rejects every action that member emits (`watch_room.go:882-885`), logged as "not allowed to control" (`:621`). The room stays alive (members keep it non-idle) yet is undrivable until the original host returns. `LeaveRoom` has the same gap (`:463-465`).

- **Unknown-room errors surface as HTTP 500 instead of a terminal 404/410**, so the client can't distinguish "room gone" from a transient fault and tight-loops (`nakama_rooms.go:108,111,155,158,240`; `join-stream` 500-spam at `nakama-4h.log:785-789, :494-497`).

- **Ghosts accumulate.** `ClientID` is never cleared on disconnect (`watch_room.go:485-501`), so departed members stay counted in `MemberCount` (`:906`) and in the auto-skip tally (`recomputeAutoSkipLocked:558-571`) forever; and the reaper deletes a room **without notifying members** (`:1107-1115`), reproducing the ghost-room strand from a mere >2min network blip.

- **Root architectural mismatch:** the client treats "in a room" as durable local state (jotai atom) while the server treats rooms as ephemeral in-memory objects, and the two are never reconciled on not-found. There is exactly one teardown signal (`NakamaWatchRoomClosed`) and it is wired only to the host-leave path (`watch_room.go:452-456`), never to "this room no longer exists."

### 2. Findings (ranked, deduped)

| # | Title | Area | Sev | Conf | Evidence | Fix |
|---|-------|------|-----|------|----------|-----|
| A | **No room persistence + no client recovery on restart** (merges F1, F9, F10, F14, F12) | persistence | HIGH | confirmed | In-mem map only, no rehydrate (`watch_room.go:236-246`, `nakama.go:256`, no model in `models.go`); reconnect re-join has no `onError` and `if(room)` guard never clears atom (`nakama-manager.tsx:636-645`, atom `:62`); `JoinRoom`→`ErrRoomNotFound` (`watch_room.go:371-375`); unknown-room returns 500 (`nakama_rooms.go:108,111,155,158`); no CLOSED event on restart; live: 8 restarts/1.5h → 500 spam (`nakama-4h.log:520-526,817`) | (1) Return 404/410 for unknown room. (2) Broadcast room-closed on shutdown + client `onError` clears `currentWatchRoomAtom`/resets modal/toasts. (3) Persist+rehydrate rooms mirroring `DebridActiveStream` (see §5). |
| B | **Host tab-close promotes control to a member without `CanControl` → room undrivable forever** (merges F6, F16) | promotion | HIGH | confirmed | Promotion never touches `CanControl` (`watch_room.go:491-497`, `970-998`); `resolveRelay` needs `IsHost \|\| CanControl` (`:882-885`); drop logged `:621`; sole-host case yields empty `ControllerKey` and prints "promoted " with blank key (`:494, :990-997`); new joiner never gets control (`:401-408`) | On promotion (both `HandleClientDisconnect:493` and `LeaveRoom:464`) set `np.CanControl=true` under `room.mu`; OR treat the current `ControllerKey` holder as always allowed in `resolveRelay`. On `JoinRoom`, if `ControllerKey==""`/offline, assign earliest live member. |
| C | **Ghost participants accumulate forever; inflate MemberCount & skew auto-skip vote** (merges F7, F8) | lifecycle | MED | confirmed | `HandleClientDisconnect` removes nothing, never clears `ClientID` (`watch_room.go:485-501`); `MemberCount=len(Participants)` (`:906`); `recomputeAutoSkipLocked` tallies all, no liveness filter (`:558-571`); reaper only deletes whole rooms (`:1079-1103`) | Clear `ClientID` on disconnect; filter member count + auto-skip tally to live/non-empty `ClientID`; add `disconnectedAt` and prune members past a grace TTL, then re-run `recomputeAutoSkipLocked`. |
| D | **Reaper deletes idle room without notifying members** (F11) | reap | MED | confirmed | `reapIdleRoomsWith` only calls `broadcastRoomsUpdated`, never `NakamaWatchRoomClosed` (`watch_room.go:1107-1115`); it holds the ClientIDs at `:1084-1092` but discards them; a >2min blip → member reconnects into `ErrRoomNotFound`, stuck like finding A | Collect participant ClientIDs before delete; after releasing locks `SendEventTo(cid, NakamaWatchRoomClosed, roomID)` — identical to host-leave path. |
| E | **Room never reaped while app ws stays open** (F13) | reap | MED | likely | `hasLive` = any participant's `ClientID` in server-wide `GetClientIds()` (`watch_room.go:1083-1096`); app ws is per-session not per-room; client sends leave ONLY from Leave/Close buttons (`nakama-manager.tsx:690,727`) — no leave-on-unmount/navigate-away | Decouple liveness from raw ws: per-room "still viewing" heartbeat, or fire `/leave` on room-panel unmount / route change, and reap on that. |
| F | **Data race on `WSEventManager.Conns`** (F15) | reap | MED | confirmed | `AddConn`/`RemoveConn` mutate `m.Conns` with NO lock (`websocket.go:167-179, 289-296`) while `GetClientIds` locks `m.mu` (`:417-419`); `GetClientIds` feeds reaper liveness (`watch_room.go:1069-1074, 950-959`) | Lock `m.mu` in `AddConn`/`RemoveConn` and every `m.Conns` mutation. |
| G | **`join-stream` returns 500 spam when host isn't streaming** (F2) | playbackActive | MED | confirmed | `nakama_rooms.go:240` `RespondWithError("the room has no active stream")`→500; josh 5×500 in ~2s after host stop (`nakama-4h.log:785-789`); same `:494,497` | Return 409/425 for "no active stream" (`:240`) and "not ready yet" (`:283`) so it isn't a logged server error and client backs off. |
| H | **Stale client 403-spams `join-stream` after host recreate** (F3) | disconnect | LOW | confirmed | After host close+recreate, josh 4×403 vs old room until manual leave (`nakama-4h.log:730-737`); handler 403 at `nakama_rooms.go:233` | Client drops room ref on 403/room-closed; folds into finding A's room-closed event. |
| I | **`roomIdleTTL=2min` can reap an in-use room on brief backgrounding** (F17) | reap | LOW | speculative | `watch_room.go:228` TTL=2min; `:1098` idle check; `lastLiveAt` only refreshes while ws in `GetClientIds()` | Longer grace (5-10min) for rooms with recent `PlaybackActive`; + CLOSED push on reap (finding D). |
| J | Discrete-drop dedup logs zero-value timestamp (`sinceLastDiscrete≈2562047h`) (F4) | playbackActive | INFO | likely | `nakama-4h.log:918` ≈ `time.Since(time.Time{})` — `lastDiscrete` never seeded | Seed `lastDiscrete` on stream/room active or treat zero as "no prior". Cosmetic. |
| K | Reaper never observed firing; those dimensions clean (F5) | reap | INFO | confirmed | 0 reap log hits in 4h; rooms ended via explicit close or restart (`:137,721,792,805-812`) | Add a DBG log when reaper actually removes a room. No defect. |
| L | VERDICT: stale `ClientID` does NOT make a truly-empty room look alive (F18) | reap | INFO | confirmed | Reaper intersects with live set (`watch_room.go:1084-1092`); stale id absent → `hasLive=false`, reaped; ids are stable per-client secrets | No change for empty-room case; still clear `ClientID` for clarity (finding C). |

### 3. Dedup pass

- **A** absorbs the four restart/persistence findings (F1 logs, F9 + F10 persistence, F14 cleanup) and the design-rec F12 (folded into §5). All describe the same failure: in-mem rooms die on restart, client never recovers, errors are 500 not typed.
- **B** merges F6 (high, promoted member lacks control) with F16 (low, sole-host empty `ControllerKey`) — one promotion defect with two entry points.
- **C** merges F7 (ghost accumulation) with F8 (ClientID lingers as broadcast/promotion target); F18 (**L**) is the verdict that this is *not* the empty-room reap bug, so it stays informational rather than contradicting C.

### 4. Do-first shortlist (highest leverage) and how it folds into the #25 redesign

Ranked by leverage-per-line:

1. **Clear `ClientID` on `HandleClientDisconnect`** (finding C/L). One edit — for each room, if a participant's `ClientID==clientID` set it `""`. Reconnect via `JoinRoom` re-sets it by key, so it's safe. This single change makes "absent" observable and turns findings C's member-count/auto-skip filters into trivial `ClientID!=""` checks, and hardens `nextControllerKeyLocked`.
2. **Grant control on promotion** (finding B). `if np := room.Participants[room.ControllerKey]; np != nil { np.CanControl = true }` at both call sites (already under `room.mu`). Unfreezes every host-tab-close room.
3. **Typed room-gone signal + client self-clear** (finding A). Server returns 404/410 for unknown room; client's reconnect `onError` clears `currentWatchRoomAtom`, resets modal, toasts. Kills the 500-spam and the frozen-panel strand.
4. **Emit `NakamaWatchRoomClosed` on reap AND on shutdown** (findings A, D). Reuse the host-leave emission with the ClientIDs the reaper already holds. Members self-clear instead of hammering a dead id.
5. **Lock `m.Conns` mutations** (finding F). Serializes the reaper's liveness read against ws churn — a panic/false-"no live" hazard, worst right after a restart when all sockets re-register.
6. **Room persistence layer** (finding A, §5). The one structural fix that makes a redeploy transparent.

**Fold into the #25 redesign (anchor/intent + actor-per-room + one engine):**
- *Actor-per-room* serializes all room mutations behind one goroutine → structurally eliminates the finding F race class and the unsynchronized `ClientID`/promotion edits (C, B) without ad-hoc locking.
- *Anchor / server-authoritative playback intent*, persisted, is exactly the serialization surface finding A needs: rehydrate on boot with `ClientID` cleared, and the existing 6s `playbackStaleTTL` self-heals `PlaybackActive`.
- *Controller-is-own-follower / logical controller role*: model control as a room-level role, not a per-participant `CanControl` bool. Promotion then inherently confers control (finding B disappears by construction), and the current holder is always allowed in `resolveRelay`.
- *One engine* = single source of truth for liveness: decouple room membership from raw app-ws connectivity (finding E) with a per-room presence heartbeat, and make room existence a typed signal the client reconciles against (findings A, D, H).

### 5. Room-persistence recommendation

**Survive restart: YES**, server-authoritative persist+rehydrate — not client-driven auto-recreate. Client recreate needs leader election among members (races, duplicate rooms), can't authoritatively restore host/controller/password/auto-skip votes, and fails entirely if the host's client is the one that didn't reconnect. The hub is already the runtime source of truth; the room struct is JSON-tagged (`watch_room.go:67-92`) with a marshal-safe `Snapshot()` (`:1058`).

Mirror the proven `DebridActiveStream` pattern (model `models.go:734`, snapshot `stream.go:1417`, write-through `persistActiveStream:1434`, boot restore `loadPersistedActiveStream:1459` gated on a TTL):

1. **Model** `NakamaWatchRoom{ID pk, Data string}` (or a single filecache blob). Store `{ID, Name, HostKey, HasPassword, passwordHash, ControllerKey, ForceHostTracks, participants(User+IsHost+CanControl+JoinedAt+AutoSkipPref), CurrentMediaInfo, paused/position/positionAt, PlaybackActive, CreatedAt}`.
2. **Do NOT persist** `ClientID` / `lastControllerClientID` — per-connection, re-earned on re-join.
3. **Write-through** best-effort on each membership/playback mutation, **debounced ~2-5s** so the 500ms broadcast/heartbeats don't churn the DB.
4. **`loadPersistedRooms()` in `NewWatchRoomHub`** (`watch_room.go:236-246`): rebuild each room with `ClientID` cleared and `lastLiveAt=now` so the 2min reaper evicts rooms nobody reconnects to. Rehydrated `PlaybackActive` is cleared by the existing 6s `playbackStaleTTL` (`:273`) if the controller never returns.
5. **Delete the row** on reap / host-close.
6. **Raise `roomIdleTTL`** (or add a post-boot restore window) so a quick redeploy doesn't reap rooms whose members simply haven't reconnected yet (finding I).

Net: with this, a ~15min auto-update is transparent as long as members reconnect within the grace window; without it, every deploy strands live parties (findings A, B, D).

