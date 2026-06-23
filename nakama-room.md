# Nakama Rooms — pool + multi-room watch parties

Reframes Nakama from **one host instance / one watch party** into a **user pool** with
**multiple watch rooms**. Goal: people using the shared VPS backend (and people on
external instances connected to it) all land in one pool, and any of them can spin up a
room others can join.

This is a design doc, not yet built. Built on top of the existing Nakama transport +
sync engine — most of it is reuse, not new code. Read alongside `profile-support.md`
(identity is the shared dependency) and the memory `profile-multiuser-support`.

---

## 1. What exists today (don't rebuild it)

Nakama already ships a full watch-together engine. Key files in `internal/nakama/`:

- **Roles**: an instance is either **host** (`settings.IsHost`, `HostPassword`) or a
  **peer** (connects to a host via `RemoteServerURL`+`RemoteServerPassword`). Host holds
  N `peerConnections` (one ws per peer instance); a peer holds one `hostConnection`.
- **One watch party per host**: `WatchPartyManager.currentSession` is a *single*
  `mo.Option[*WatchPartySession]`. Exactly one party per manager. Host
  `CreateWatchParty()`, peers send `watch_party_join`.
- **Playback source today = the host's *own* player.** `listenToPlaybackAsHost`
  subscribes to the host's `playbackManager` + `videoCore`; the host plays the video and
  its status is broadcast to peers, who sync. Control is host→peers only.
- **Relay mode** already does the thing we need most: a chosen **peer ("origin")**
  drives playback, and the host just **forwards** origin→peers without playing anything
  itself (`StreamType` file/torrent/onlinestream = host "does nothing", only relays
  status). This is the template for "a browser drives, the server forwards."
- **Sync engine** (`watch_party_syncing.go`): drift detection, seek/catch-up, buffering
  gate (`waitForPeersReady`, `checkAndManageBuffering`), sequence ordering. Reusable as-is.
- **Participants** carry `CanControl bool` **already** — only set `true` for host today.
  The permission scaffolding is half-present.
- **Sync payload is position-only**: `WatchPartyPlaybackStatus{Paused, CurrentTime,
  Duration}`. No audio/subtitle track in it — per-user tracks already "just work" as long
  as we don't add tracks to the sync messages.
- **Transport**: direct (peer↔host ws) and "Rooms" relay (host opens a room on upstream's
  hosted `SeanimeRoomsApiUrl`, peers tunnel through it). Both reusable.
- **Frontend**: `seanime-web/.../_features/nakama/nakama-manager.tsx` (859 lines) +
  `nakama-watch-party-chat.tsx`, hooks in `api/hooks/nakama.hooks.ts`
  (`useNakamaCreateWatchParty`, `useNakamaJoinWatchParty`, `useNakamaRoomsAvailable`,
  `useNakamaCreateAndJoinRoom`, ...). The room vocabulary partly exists on the client.

**Two structural assumptions block the new model:**
1. The server models **one** active UI client (`Manager.clientId`, `useDenshiPlayer` are
   single-valued). There's no concept of "the set of local users."
2. There's **one** session, not a set of rooms.

---

## 2. Target model

### 2.1 The pool
The hosting instance (our VPS) maintains a **user pool** = everyone it can see:

- **Same-instance users** — every web/Denshi client connected to *this* backend is
  automatically a pool member. No host/peer handshake; they're already on this server.
  Each becomes a pool user once their UI websocket is up. Identity (display name, and
  later profile) comes from the profile/multi-user layer.
- **External-instance users** — another Seanime instance connects peer→host (today's
  mechanism). Its user(s) get appended to *this* host's pool.

Pool = `{local clients of this backend} ∪ {users of connected external instances}`.
The **VPS instance is the hub**: it owns the canonical pool and the canonical room list.
External instances are leaves that contribute users and mirror the hub's room list — they
do **not** aggregate each other (single-hub, no federation; see Deferred).

**Identity (decided).** A pool user's ID is their **profile username** (the profile/
multi-user layer is stable enough for this). To avoid an external user colliding with a
local user of the same name, **external users are namespaced by their origin server**:
local = `username`, external = `<serverTag>-username` (e.g. `external1-alice`). The
display name stays the bare username; the namespaced form is only the internal key. The
pool stores `{userId, displayName, source: local|external, serverTag, useDenshiPlayer}`.

**Build order (decided).** Ship the **same-instance** pool first — it covers the main
use case (several users on the one VPS). But model the pool source as an abstraction
(`source: local|external`, `serverTag`) from day one so external contribution wires in
without reshaping the registry. Same goes for the room registry: roomIds and participant
keys are source-agnostic from the start.

### 2.2 Rooms
- Any pool user can **create a room**; the creator is that room's **host**.
- A room may be **open** or **password-protected** (host's choice at creation).
- Multiple rooms coexist. A user is in **at most one** room at a time (v1).
- A room has: `id`, `hostUserId`, `name`, optional `passwordHash`, `participants` (subset
  of pool, each with control perms), `currentMediaInfo` + playback state, `createdAt`.

### 2.3 Discovery / visibility
- **Nakama is on by default** for every user (sidebar icon + settings entry). A user can
  **opt out** in settings. Being in the pool is the default state.
- **Rooms are visible pool-wide; users are not.** There is **no global userlist** — you
  never see "who else is in the pool." The only place a member list appears is **inside a
  room** you've joined. So the pool grants *eligibility* (create/join rooms), not presence.
- The Nakama modal shows **available rooms** with a join button; locked rooms show a lock
  and prompt for a password (inline) on join.
- Room visibility rule: you see the rooms of the hub you're attached to. **Local users**
  see this instance's rooms. **External users** see this host's rooms **only while
  connected** to it. (VPS = host mode → external users connected to it see its rooms.)
- Room-list changes are pushed: to local clients via `WSEventManager`, to external
  instances via a Nakama message that they re-emit to their own local clients.
- Consequence for control perms (§2.4): since the host can't see a global userlist, they
  grant control to **members who have joined their room**, not to arbitrary pool users.

### 2.4 Control permissions
- Default: only the room host controls play/pause/seek (today's behavior).
- The room host gets a **per-room control panel** listing the **room's joined members**
  (not the global pool — there's no global userlist) and grants control to **specific
  members** or **everyone**. Backed by per-participant `CanControl` (already on the struct).
- Lazy default (confirmable, Q5): **one `CanControl` boolean** covers play/pause + seek +
  episode-change together. Split into separate toggles only if you want finer control.
- Any participant with control can drive; their action propagates to **all** members
  including the host → control becomes **multi-source**, where today it's one-way.
- Lazy conflict policy (confirmable, Q6): **last-write-wins by sequence/timestamp** — the
  sync engine already orders messages this way, so two near-simultaneous controllers
  resolve deterministically with no new machinery.

### 2.5 Per-user tracks
- Subtitles and audio track are **always local** to each watcher. Sync carries only
  media identity + position + play/pause. No code forces tracks today; the requirement is
  "keep it that way" + expose the normal player track menus during a room session.

### 2.6 Host presence & promotion (decided)
- **Stream never stops** when the host disconnects — peers keep playing.
- On host disconnect, **auto-promote the next member by join order** (host = 1st joiner →
  promote the 2nd, then 3rd, …) to temporary controller so the room keeps driving.
- The temp host is *temporary*. When the **original host reconnects**, control returns to
  them automatically (no need to re-promote an auto-promoted temp host). The returning host
  **syncs to the room's current position** and resumes driving from there — peers are not
  rewound.
- Implementation note: track `hostUserId` (original) separately from `currentControllerId`
  (effective driver). Promotion only moves `currentControllerId`; reconnect restores it to
  `hostUserId`. Join order is already recoverable from participant `LastSeen`/insertion —
  store an explicit `joinedAt` to be safe.

### 2.7 Stream sources (decided)
- Debrid / torrent / onlinestream: every member fetches the same stream independently from
  shared media identity — full feature support.
- **Local files: allowed, but degraded.** A file that exists only on the host can't be
  played by members who don't have it. Surface this clearly in the room UI ("local-file
  room — others need the same file / limited support") rather than blocking it.

---

## 3. Reuse map (lazy core)

| Need | Reuse | New |
|---|---|---|
| Transport (ws, rooms relay) | direct + Rooms relay | — |
| Browser drives, server forwards | **relay mode** (origin→host→peers) | generalize origin = room host |
| Drift/seek/catch-up/buffer sync | `watch_party_syncing.go` | scope it per-room |
| Chat | `SendChatMessage` | scope it per-room |
| Per-participant control flag | `CanControl` | UI + enforcement for non-hosts |
| Position-only sync payload | `WatchPartyPlaybackStatus` | — (keeps tracks local) |

**The lazy framing: a room ≈ a relay-mode session whose origin is the room-host's
browser.** Each room is essentially relay mode generalized. We are not writing a new sync
engine; we're (a) letting the server hold *many* relay sessions instead of one, (b)
sourcing the origin from a local browser client rather than only an external peer, and (c)
allowing >1 controller.

---

## 4. New backend pieces

1. **Local client registry** (`Manager`): track every connected UI websocket as a pool
   user `{clientId, userId/displayName, useDenshiPlayer}`. Feed it from the existing
   per-client ws subscription (`WSEventManager`). Replaces the single-valued
   `clientId`/`useDenshiPlayer`. **Depends on profile/multi-user identity** for stable
   user IDs (Q1).
2. **Room registry** (`WatchPartyManager` → `RoomManager`): `map[roomId]*Room` replacing
   the single `currentSession`. Most session methods become room-scoped (`CreateRoom`,
   `JoinRoom`, `LeaveRoom`, `ListRooms`, per-room broadcast). Relay/buffer/sync state moves
   onto the `Room`.
3. **Pool + room broadcast**: send room-list + room-state to all pool members — local via
   `WSEventManager`, external via Nakama messages the leaf re-emits.
4. **Multi-source control**: accept play/pause/seek from any participant with `CanControl`;
   re-broadcast to the whole room; define a conflict policy (Q6).
5. **Control-permission API**: room host sets per-user / all-users control.

Existing single-session message types (`watch_party_*`) gain a `roomId`. The wire format
is otherwise unchanged.

---

## 5. Frontend (fold into existing UI, minimal new surfaces)

Constraint: build into the **current** Nakama modal + player; avoid new modals/popups.

- **Nakama modal** (`nakama-manager.tsx`): add a **Rooms** section — a list of room cards.
  A single inline "Create room" row (room name + optional password toggle) instead of a
  separate modal. Password entry on join is an inline field on the card, not a popup.
- **Room card layout** (match Seanime styling):
  - Optional **cover image** of the current show, if it fits cleanly.
  - **Main heading** = room name. **Subtext** = `Host: <username>`.
  - **Current show / episode** line (e.g. `Frieren · E12`) when the room is playing.
  - **Top-right**: yellow **lock icon** if password-protected.
  - **Bottom-left**: member count. **Bottom-right**: **Join** button.
- **In-room**: reuse the existing watch-party panel + `nakama-watch-party-chat.tsx`. Show
  members and who currently has control.
- **Host control panel**: a small inline section in the same panel — a member list with
  a per-member control toggle + an "allow everyone" switch. No separate dialog.
- **Tracks**: rely on the normal player audio/subtitle menus; just don't disable them in a
  room. Nothing new.

Hooks already hint at this (`useNakamaRoomsAvailable`, `useNakamaCreateAndJoinRoom`) — wire
them to the new room registry rather than the single-session endpoints.

---

## 6. Deferred (ponytail: don't build until asked)

- **Federation / pool-of-pools**: multiple hubs aggregating each other. Single-hub only.
- **Multiple rooms per user simultaneously**: one room per user in v1.
- **Persistent rooms** across restarts: rooms are in-memory, die with the process.
- **Moderation/kick/ban**, room capacity limits, invite links.
- **Server-as-player rooms**: the server never plays; a room always has a browser origin.

---

## 7. Decisions & open items

**Decided** (baked into §2 above):
- Identity = profile username; external users namespaced `<serverTag>-username` (§2.1).
- Same-instance first, external wired in later via a source abstraction (§2.1).
- Nakama on by default, opt-out in settings (§2.3).
- No global userlist — rooms are pool-visible, members visible only inside a room (§2.3).
- Host disconnect: stream continues, auto-promote by join order, original reclaims on
  reconnect and syncs to current position (§2.6).
- Local-file rooms allowed but degraded, clearly labelled (§2.7).

**Lazy defaults set, confirm if you disagree:**
- Q5 control granularity → one `CanControl` boolean (play/pause+seek+episode together).
- Q6 multi-controller conflict → last-write-wins by sequence/timestamp.

**Resolved — "pool but not visible" model (§2.3):**
- Discovery is self-serve; no invites. Hosts create rooms, others find + join them.
- Password rooms are visible-but-locked (yellow lock on the card).
- Host username and room name are shown on the card (§5 room-card layout).
- Rooms advertise immediately on creation (host alone is fine).

Spec is complete enough to start the same-instance build.
