# Nakama watch-together parity — web (seanime-web) vs Tenji

Scope: `seanime-web/src/app/(main)/_features/nakama/` @ HEAD (commit 7dec652f "full-fork audit —
... MpvCore watch-room sync (3.9.5)") vs `seanime-tenji/src/lib/nakama/` + `src/components/features/nakama/`
@ HEAD 65f8baf (v0.1.24). Focus: does Tenji implement the CURRENT sync protocol semantics
(host/follower roles, seek cooldowns, buffering holds, non-participant guard)?

## Files compared

Web:
- `seanime-web/src/app/(main)/_features/nakama/nakama-manager.tsx` (modal, legacy host/peer UI, WatchRoomsSection)
- `seanime-web/src/app/(main)/_features/nakama/nakama-room-sync.ts` (useWatchRoomPlayerSync, useRoomStreamJoin)
- `seanime-web/src/app/(main)/_features/nakama/nakama-watch-party-chat.tsx` (legacy watch-party chat, mounted in main-layout.tsx)
- `internal/handlers/nakama_rooms.go`, `internal/nakama/watch_room.go` (backend, for the IsParticipant / e0edcaf3 checks)

Tenji:
- `src/lib/nakama/watch-room.ts` (atoms, useWatchRoomFollow, useWatchRoomLiveState, useWsLatencyProbe, useRoomStreamJoin)
- `src/lib/nakama/use-watch-room-sync.ts` (useWatchRoomSync — the player-attached sync hook)
- `src/lib/nakama/ws-latency.ts`
- `src/components/features/nakama/watch-rooms-sheet.tsx` (discovery + in-room panel)
- `src/components/features/nakama/room-stream-join-fab.tsx` (global "Join room stream" FAB)
- Wired in: `app/(app)/_layout.tsx` (useWatchRoomFollow/useWatchRoomLiveState/useWsLatencyProbe, RoomStreamJoinFab)
  and `app/(app)/(media)/player.tsx` (useWatchRoomSync(player))

## Commit-by-commit trace

### 7dec652f — "MpvCore watch-room sync (H1)" rework of nakama-room-sync.ts

This commit's 131-line diff is almost entirely about bridging TWO web player backends
(VideoCore DOM `<video>` element vs MpvCore native/Electron IPC) behind a single
`vc_globalPlayerSyncControl` abstraction (`PlayerSyncControl` interface: `.domElement`,
`.subscribe()`, `.seek()`, `.setPlaybackRate()`, etc.) — because MpvCore playback (Denshi) had
been silently NOT wired into the room-sync hook since the v3.9 merge (dead sync on Denshi).

Tenji has only ONE player backend (native mpv via expo-mpv-player, wrapped by the `SyncPlayer`
type: `state`, `source`, `play/pause/seekTo/setSpeed/setAudioTrack/setSubtitleTrack`), so the
dual-backend abstraction itself is N/A — there's nothing to "bridge" since Tenji's player
object is passed directly into `useWatchRoomSync(player)`. The RELEVANT semantic content of the
commit (the things that actually matter for correctness) were already carried by Tenji's own
prior commits (see below) or are present in the current file:

- Discrete play/pause event-driven emit: web uses DOM events / MpvCore subscribe(); Tenji derives
  emits from `state.paused` toggling (React effect) + a wall-clock-vs-position-jump heuristic for
  seeks (`lastTick` effect, `LOCAL_SEEK_THRESHOLD = 1.5`) since MPV has no discrete "seeked" event
  either on Electron OR React Native — same design as web's own MpvCore branch. PRESENT.
- Buffering hold ("stalled" heartbeat → report as paused so followers HOLD not rewind): PRESENT
  in Tenji, `heartbeatRef.current` — `stalled = !state.paused && (state.status === "buffering" ||
  state.status === "loading")`. Matches web's `stalled = heartbeat && !p.paused && (p.seeking ||
  p.readyState < 3)` semantics 1:1 (buffering/loading ~ seeking/readyState<3).
- "Driver far behind us -> never rewind" (rubber-band fix): PRESENT in Tenji (`drift <
  -HARD_SEEK_DRIFT` branch, `setSpeedSafe(1)`, no seek) — ported earlier in Tenji commit
  9f8656d (mirrors web's 703e010c).
- Non-local-controller-guard removed from apply path (Issue-A fix, "apply unconditionally, the
  source isn't us"): PRESENT in Tenji, same code comment verbatim in `use-watch-room-sync.ts`
  around the `useRoomWsListener<RoomPlaybackSync>(ROOM_PLAYBACK_SYNC, ...)` apply handler.
- Echo suppression (state-matched, not blind time window): PRESENT, `lastAppliedRef` /
  `APPLY_ECHO_WINDOW_MS` = 2500 (matches web's `APPLY_ECHO_WINDOW_MS`).
- Half-RTT lead compensation (uplink while emitting, downlink while applying):
  PRESENT — `getHalfRttSeconds()` used identically in both emit and apply paths, backed by
  Tenji's own `ws-latency.ts` + `useWsLatencyProbe` (5s ping interval vs web's own RTT probe
  elsewhere in websocket handling — same mechanism, separately implemented for RN's raw socket).
- Teardown emit guard ("player reset to t=0 on unmount must not seek followers to start"):
  web implements this HEURISTICALLY inside the generic `emit()` — it detects "t≈0 after
  meaningfully progressing" and drops the emit. Tenji does NOT need the heuristic because its
  architecture is different: Tenji's `emitStop()` is called EXPLICITLY from
  `player.tsx`'s `handleBack()` (host-only) with an explicit `suppressEmitUntilRef.current =
  Date.now() + 2000` window before sending the stop payload — a more deliberate mechanism for
  the same problem (stray post-close play/pause events don't leak out during the 2s window).
  Functionally equivalent, arguably more robust since it's not inferring intent from a magic
  t<0.5 heuristic. PORTED (different mechanism, same guarantee).
- **GAP FOUND: `SEEK_COOLDOWN_MS` (2500ms) hard-seek throttle is MISSING in Tenji.**
  See below — this is NOT part of 7dec652f itself; it's from an EARLIER web commit
  (159a4efe) that never made it into Tenji at all. Web's apply-listener heartbeat branch:
  ```
  } else if (drift > HARD_SEEK_DRIFT && (Date.now() - lastHardSeekRef.current) > SEEK_COOLDOWN_MS) {
      action = `seek->${target.toFixed(1)}`
      player.seek(target)
      player.setPlaybackRate(1)
      lastHardSeekRef.current = Date.now()
  }
  ```
  Tenji's equivalent branch (`use-watch-room-sync.ts` line ~254):
  ```
  } else if (drift > HARD_SEEK_DRIFT) {
      // We fell BEHIND the driver -> snap forward.
      action = `seek->${target.toFixed(1)}`
      player.seekTo(target)
      setSpeedSafe(1)
  }
  ```
  No `lastHardSeekRef` / cooldown gate at all — confirmed via
  `grep -n "COOLDOWN\|lastHardSeek\|HARD_SEEK_DRIFT" src/lib/nakama/use-watch-room-sync.ts`
  → only the `HARD_SEEK_DRIFT` constant appears, no cooldown ref.

  Traced the web fix to commit 159a4efe "fix(nakama/rooms): throttle follower hard-seeks to
  spare the directstream" (2026-06-24), predating 7dec652f. Its own commit message: "the
  directstream serves ONE byte-range at a time and a SEEK cancels the in-flight range, killing
  the connection and forcing a rebuffer. A follower that hard-seeks on every heartbeat drift
  would thrash its own stream (seek->reset->rebuffer->fall behind->seek)." This applies
  identically to any client following a debrid/torrent HTTP directstream — including Tenji,
  which follows via `joinStream({ ..., playbackType: "externalPlayerLink" })`, i.e. mpv opens a
  server-provided URL that, absent a direct-CDN link, is the same directstream proxy.

  Checked Tenji's git log for `use-watch-room-sync.ts`: commits 818f04e (initial port), 24df0b8,
  7ad2cd6, 20408a6, 89a3dac, 84502da, 867de56, cf8c971, 9f8656d. The commit that added the
  "no rewind to stalled driver" + "host-only stop" fix (9f8656d) mirrors web's 703e010c, but
  there is NO Tenji commit mirroring web's 159a4efe (the throttle-specific commit) — it was
  simply never ported. This is a real, live gap: a Tenji follower whose player falls >0.6s
  behind the driver (common on a rebuffer) will hard-seek EVERY heartbeat (1s) until it catches
  up, with no cooldown, which is exactly the thrash pattern 159a4efe fixed for Denshi/web.

### e0edcaf3 — "fix(namaka): don't interfere with players of peers not in watch party"

This is a pure backend Go fix in `internal/nakama/watch_party_peer.go`
(`handleWatchPartyStateChangedEvent`): it gates "host stopped session -> cancel my playback" on
`isParticipant` (this peer must actually be `currentSession.Participants[hostConn.PeerId]`)
before tearing down local playback. This is the LEGACY watch-party (host/peer, WatchPartyManager)
system, NOT the newer same-instance "rooms" system. It's 100% server-side enforcement with no
client-side surface — it protects ALL clients (web, Denshi, Tenji) identically regardless of
what UI they run, because the guard lives in the peer-to-peer session state machine on the
server. N/A for client-side parity; nothing to port. (Confirmed Tenji has no legacy watch-party
host/peer implementation at all — grep for `currentWatchPartySession`/`WatchPartyManager`-style
code in Tenji returns nothing; Tenji only implements the newer same-instance "rooms" model,
which is consistent with web's own UI: `nakama-manager.tsx` literally comments "Watch Party
(legacy peer/host) removed — Watch Rooms (top) replaces it" for the room UI section, i.e. rooms
are the primary/current UX on web too.)

Similarly checked the ROOMS-specific membership gate ALSO added in 7dec652f itself
(`internal/handlers/nakama_rooms.go` `HandleNakamaWatchRoomJoinStream` — `IsParticipant` check
added so a non-member can't piggyback a controller's already-resolved debrid stream by knowing
a broadcast roomId) — also pure backend, protects Tenji automatically.

## Rooms/pool UI state — feature-by-feature

Compared `nakama-manager.tsx`'s `WatchRoomsSection` (web) against `watch-rooms-sheet.tsx` +
`room-stream-join-fab.tsx` (Tenji):

| Feature | Web | Tenji | Status |
|---|---|---|---|
| Room discovery list (cards: cover, name, host, title/ep, member count, lock icon) | yes | yes (`RoomCard`) | ported |
| Create room (name + optional password) | yes | yes (`CreateRoomRow`) | ported |
| Join room (+ password prompt if locked) | yes | yes | ported |
| Live room-list push (`NAKAMA_ROOMS_UPDATED` / `ROOMS_UPDATED`) | yes | yes | ported |
| In-room member list w/ host crown + "driving"/controlling badge | yes | yes (`MemberRow`, "driving" label) | ported |
| Host: per-member control toggle | yes | yes | ported |
| Host: "everyone can control" / "host only" toggle | yes | yes (`everyoneControls` derived + toggle) | ported |
| Host: force my audio/subtitle tracks toggle | yes | yes | ported |
| Auto-skip OP/ED: per-user vote (on/off/auto) + effective state + on/off counts | yes | yes | ported |
| Leave room (host = "close room" wording) | yes (button label changes) | Tenji always shows "Leave" (no host-specific relabel) but functionally same leave call | ported (cosmetic diff only) |
| Global floating "Join room stream" FAB when a live stream exists you're not watching | yes | yes (`RoomStreamJoinFab`, positioned bottom-right, mounted app-wide in `_layout.tsx`) | ported |
| Reconnect re-join on websocket reconnect (reclaims control, empty password ok) | yes (`prevConnectedRef` effect in `nakama-manager.tsx`) | yes (`rejoinedSocketRef` effect in `watch-room.ts` `useWatchRoomFollow`, keyed on new socket object since RN makes a fresh WebSocket per reconnect) | ported |
| Server-side room-reconnect handling (`ROOM_RECONNECTED`) | yes | yes | ported |
| Host closes room -> members drop out + stop playback + toast | yes | yes | ported |
| Debug diagnostic channel (`NAKAMA_ROOM_DEBUG` / `ROOM_DEBUG`) | yes (temporary/diagnostic) | yes (`useRoomDebug`, described as "no console on iOS" workaround) | ported |
| Legacy host/peer federation UI (external connections, passcode, reconnect/cleanup) | yes (still shown when `showLegacy`) | not implemented | N/A — legacy system is being deprecated on web itself; rooms are the current primary UX; not part of the "current sync protocol" scope |
| Watch-party text chat (legacy watch-party only, not rooms) | yes (`nakama-watch-party-chat.tsx`, mounted for legacy sessions) | not implemented | N/A for the same reason (chat is tied to the legacy watch-party session, not to rooms; rooms have no chat feature on web either) |

## Secondary observation (not scored — outside "sync protocol" scope but adjacent)

Web's `startRoomStream`/join-stream call passes `directCdnCapable: __isElectronDesktop__` to
`useNakamaJoinWatchRoomStream`. Tenji's equivalent calls (`watch-room.ts` `maybeFollow` and
`useRoomStreamJoin`) call `joinStream({ roomId, clientId, playbackType: "externalPlayerLink" })`
with NO `directCdnCapable` field at all. Did not chase this further (it's a join/CDN-handoff
concern, not sync-tick semantics), but flagging it because if the field defaults falsy/absent
server-side, Tenji followers may always be routed through the directstream proxy rather than a
direct CDN link — which would make the missing SEEK_COOLDOWN_MS gap above bite MORE often on
Tenji than a hypothetical CDN-direct path would. Worth an separate check by whoever owns the
CDN-handoff/join-stream area.

## Verdict

- Host/follower roles, echo suppression, half-RTT lead, buffering-hold, non-participant guards,
  teardown handling, and the full rooms/pool UI (discovery/create/join/members/host-panel/
  auto-skip vote/join-FAB/reconnect) are all correctly ported and match current web semantics.
- One real, confirmed gap: **the SEEK_COOLDOWN_MS (2500ms) post-hard-seek throttle is missing**
  from Tenji's apply-listener heartbeat branch, present in web since commit 159a4efe (predates
  the 7dec652f MpvCore rework, was never ported to Tenji at all). Effort: S (single `useRef` +
  one `Date.now()` guard condition, directly mirrors the web code already read above).
