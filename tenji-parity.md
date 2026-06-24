# Tenji parity — bringing the iOS client up to date

> ## SESSION-3 HANDOFF (2026-06-23, later) — watch-room PLAYBACK control (start/stop/close/reconnect)
>
> **✅ TENJI PORTED + tsc-clean + pushed (commit `24df0b8`).** Needs a fresh IPA build + a live
> Denshi↔Tenji test. Implementation: app-wide `useWatchRoomFollow()` in `src/lib/nakama/watch-room.ts`
> (auto-start via playback-intent + entry navigation, stop/close via `watchRoomTerminateSignalAtom`,
> reconnect re-join on clientId change); `emitStop` in `use-watch-room-sync.ts` wired to the player's
> back; `player.tsx` watches the terminate signal. The spec below is retained for reference.
>
> **Seanime side: all LIVE on the VPS** (committed `c4bef93d`, pushed to fork; arm64 deployed, 200).
> Denshi installer rebuilt: `seanime-denshi/dist/seanime-denshi-3.8.7_Windows_x64.exe`.
>
> Session-2 ported room *sync* (apply seek/play/pause to an already-playing MPV stream). That's
> necessary but NOT sufficient — in cross-platform testing the peer never STARTED playback. This
> round closes four gaps. **All backend is server-side & deployed (Tenji gets it free); the items
> below are the CLIENT behaviors Tenji must add.** Web reference: `seanime-web/.../nakama/
> nakama-room-sync.ts` + `nakama-manager.tsx` (`WatchRoomsSection`), `native-player.atoms.ts`
> (`nativePlayer_terminateRequestedAtom`).
>
> **0) Generated-types delta (do first):** `Nakama_RoomPlaybackStatusPayload` gained
> `streamType: Nakama_WatchPartyStreamType` ("file"|"torrent"|"debrid"|"onlinestream"),
> `aniDbEpisode: string`, and **new** `stopped?: boolean`. Re-sync these fields. Add WS const
> **`nakama-watch-room-closed`** (server→client).
>
> **1) Peer AUTO-START (the actual fix — "host starts → peers start"):** the relayed status now
> carries `streamType` + `aniDbEpisode`, so a follower can launch the SAME source.
> - **Emit side (controller):** populate `streamType` + `aniDbEpisode` from the active MPV
>   playback descriptor. Web maps its player streamType → room type: `localfile→file`,
>   `torrent→torrent`, `debrid→debrid`, else "". `aniDbEpisode` from the episode being played.
> - **Apply side (follower):** on each incoming `nakama-room-playback-sync` AND on late-join
>   (`room.lastPlayback`), if `p.mediaId/p.episodeNumber` is set, `!p.stopped`, the streamType is
>   **debrid or torrent**, and we're NOT already playing that exact media+episode → start it via
>   Tenji's MPV stream-start path (the auto-select equivalent of web's
>   `handleAutoSelectStream({mediaId, episodeNumber, aniDBEpisode})` — i.e. `debrid/stream/start`
>   or torrent stream start with `autoSelect:true`). Then the existing position-sync takes over
>   once MPV loads. Guard: a `lastStartedKey` ref (`mediaId:ep:streamType`) + the start mutation's
>   isPending so a burst of syncs doesn't relaunch. Skip `file`/`onlinestream` (can't share
>   cross-device). See web `maybeAutoStart`.
>
> **2) Peer STOP ("host stops episode → peers stop"):** mirror of auto-start.
> - **Emit:** when the controller's MPV player closes/ends (goes inactive) while in a room +
>   controlling, send `nakama-room-playback-status` with `{stopped:true, mediaId:0, ...}`. An
>   episode SWITCH must NOT trip this (only a real stop/close).
> - **Apply:** on a sync with `stopped:true`, tear down MPV (stop playback / leave player). Set the
>   echo guard window to ~2s so your own teardown doesn't re-emit a stop.
>
> **3) Host CLOSE ROOM → members drop + stop:** backend now closes the whole room when the HOST
> leaves (non-host leave just removes that member) and pushes **`nakama-watch-room-closed`**
> (payload = roomId string) to the others. Tenji: on that event, if it's our current room → clear
> the current-room atom, stop MPV, toast "host closed the room". (The host's existing "Close room"
> = Leave endpoint already triggers it server-side — no new call needed.)
>
> **4) RECONNECT reclaim (host control came back wrong):** a WS reconnect issues a NEW clientId, so
> the server was still broadcasting to the host's dead client and control stayed handed-off. Fix is
> **client re-join on reconnect**: when `clientId` changes while in a room, call
> `useNakamaJoinWatchRoom({roomId, password:"", clientId})` and update the current-room atom. This
> remaps the driving client (sync reaches the host again) AND reverts control to the host. Backend
> `JoinRoom` now skips the password check for an existing member, so empty-password re-join is safe.
> Mirror web's effect in `nakama-manager.tsx` (`prevClientIdRef`). **Temporary disconnect is already
> safe server-side** (the participant isn't removed; control hands off and is reclaimed on re-join).
>
> **Order:** (0) types/WS const → (4) reconnect re-join (cheap, high-value) → (1) auto-start (the
> headline fix; needs Tenji's MPV stream-start entry point, likely `use-torrent-stream-controller.ts`)
> → (2) stop → (3) close. Then live Denshi↔Tenji test: host start→peer starts, host stop→peer stops,
> host close→peer drops, host reconnect→control returns.
>
> ---

> ## SESSION-2 HANDOFF (2026-06-23, late) — what Tenji needs after this round
>
> **All of the below is now LIVE on the VPS** (server backend + browser/Denshi web). The
> Denshi installer was rebuilt: `seanime-denshi/dist/seanime-denshi-3.8.7_Windows_x64.exe`
> (carries rooms UI + the reconnect-resume below). Seanime working tree is uncommitted.
>
> **Server-side (Tenji gets these FREE — no client work):**
> - **CDN-429 stall fix** (`httpstream.go`): retry/backoff on 429/5xx + `MaxConnsPerHost:8`.
>   Was causing "stream stops mid-episode" (TorBox CDN throttling). Tenji's MPV streams hit
>   the same server → fixed automatically.
> - **Live progress save** (`progress_tracking.go`): resume position now persisted every ~10s
>   during playback (not just on stop), so a crash/kill keeps your spot. Server-side → Tenji
>   benefits automatically.
> - `urlRefreshTTL` 15m→2h (minor).
>
> **CLIENT-side — Tenji MUST PORT this one:** ✅ PORTED 2026-06-23 (commit `1129a51`, in the
> IPA build alongside rooms). `src/lib/player/debrid-reconnect.ts` (`lastDebridStreamStartAtom`
> + `useDebridReconnectResume()`, mounted in `app/(app)/(media)/player.tsx`); capture at both
> debrid start sites in `use-torrent-stream-controller.ts`; `session.ts` external-player handler
> made **idempotent** (skip MPV reload when the resolved URL is unchanged) so mobile WS blips
> (backgrounding/network switch) don't reload mid-playback — only an aged/refreshed URL reloads
> + resumes. **Progress tracking needed NO port:** the server's `af16ba17` live-save is the
> playback-MANAGER path, which Tenji cancels; Tenji already reports live via **continuity**
> (~15s + on pause + on background), persisted server-side immediately — which is the position
> the resume reads. tsc-clean.
> - **Debrid reconnect-resume (1b)** — when the server restarts mid-playback (deploy/crash),
>   auto re-issue the stream + resume. **Web impl to mirror in Tenji:**
>   `seanime-web/.../debrid-stream/_lib/handle-debrid-reconnect.ts` — `lastDebridStreamStartAtom`
>   captures the last non-preload `DebridStartStream` variables (set in `handle-debrid-stream.ts`);
>   `useDebridReconnectResume()` (mounted in `native-player.tsx`) watches the WS-connected atom +
>   native-player active state, and on **reconnect after a drop while a stream was active**,
>   re-issues `startStream({...lastStart, preload:false})`. Server reuses the deduped/added
>   torrent (no new createtorrent); player resumes via continuity (now kept fresh by the live
>   save above). **Tenji port:** capture the last MPV stream-start vars in an atom; on
>   `websocketAtom` reconnect while the MPV player is active (or on a network error), re-issue
>   the same `debrid/stream/start` and let MPV resume from the continuity position. Guard with a
>   "dropped-while-active" ref so it only fires after a real drop (no loop).
> - **1a (persist active stream — server-side, Tenji gets it FREE):** new `DebridActiveStream`
>   DB model + `persistActiveStream`/`loadPersistedActiveStream` in `debrid/client/stream.go`.
>   On stream start the resolved CDN link + selection is snapshotted to the DB; on
>   `StreamManager` init (`smFor`) it's restored into the preload cache (if within its selection
>   TTL), so a re-issue after restart reuses the cached link INSTANTLY (no auto-select search) —
>   the URL is reused directly while fresh, or cheaply re-resolved from `torrentItemId` (no
>   createtorrent) if aged. This is what makes 1b's reconnect *seamless* rather than a
>   few-second re-buffer. Server-side → Tenji benefits automatically once 1b is ported.
>
> **Rooms:** already ported (status block below). Re-verify the ported room hooks/types against
> the now-deployed backend (the room endpoints are live and return 401 unauthenticated).
>
> ---

> **STATUS (2026-06-23): Nakama rooms ported to Tenji + verified (tsc clean).**
> Done this session — §1 generated layer (room types/endpoints/_Variables, added
> **surgically**, NOT wholesale: Tenji had diverged — own profile fields + `MainServerTorrentStreaming`,
> and uses `Status_UserDebrid` where seanime now has `UserDebridStatus`); §2.2 all 7 room hooks;
> §2.1 WS constants + a Tenji-native WS bridge (`src/lib/nakama/watch-room.ts` — no per-component
> pub/sub existed, so `useRoomWsListener`/`useRoomWsSender` read the raw socket from `websocketAtom`);
> §2.3 rooms UI (`src/components/features/nakama/watch-rooms-sheet.tsx`, a bottom-sheet opened from the
> Profile tab → "Watch together › Watch rooms"); §2.4 MPV sync (`src/lib/nakama/use-watch-room-sync.ts`,
> mounted in `app/(app)/(media)/player.tsx`) — emits on play/pause + **seek detected by diffing
> onProgress jumps vs wall-clock** (MPV has no "seeked" event), applies via `seekTo`/`play`/`pause`
> with an 800ms echo guard, force-tracks via `setAudioTrack`/`setSubtitleTrack`; §2.5 autoskip
> (controller-only + vote result, follower skip-buttons suppressed). App-wide room-state stays fresh
> via `useWatchRoomLiveState()` in `app/(app)/_layout.tsx`.
>
> **Not done:** §3 profile/debrid deltas (the `Status_UserDebrid`↔`UserDebridStatus` rename drift is
> untouched — defer). **Blocking for live test:** the seanime room backend is built+green but **NOT
> yet deployed to the VPS** (still uncommitted working tree); deploy it before any Denshi↔Tenji test.
> Tenji changes are uncommitted + not yet built into a native app.


What needs to come over to **Tenji** (`H:/Projects/seanime-tenji`, RN/Expo, native **MPV**
player, fork=`ClinShaiju/seanime-tenji`) to match seanime's last ~2 days of work, and to
enable **cross-platform Nakama testing** (Denshi ↔ Tenji) — the immediate goal, since two
Denshi clients need two machines but Denshi+Tenji is one phone + one PC.

This is a plan, not yet done. Tenji is a **thin client** over the same server: it has no Go,
so only **client-relevant** deltas matter. The API layer is **hand-synced** from
`seanime-web/src/api/generated` ("surgically" — see memory `tenji-ios-client`).

Current Tenji state (assessed): git tip `2cd9fe1` (profile login + gating + sign-out);
generated types last synced ~Jun 22; has `userRole`/`Models_Theme` but **missing**
`UserDebridStatus` and **all** Nakama room types; has nakama *hooks* (old watch-party only),
**no nakama UI**; player = `modules/expo-mpv-player` (`play()`, `pause()`, `seekTo(pos)`,
`seekBy`, `onProgress`).

---

## 0. What's NOT needed (backend-only / web-only — skip)

Most of the seanime commit window is server-side or web-specific; Tenji gets the backend for
free by talking to the same server. Explicitly skip:

- All Go backend commits (per-user sessions, streaming isolation, debrid perf, autoselect
  ranking, username-in-logs, etc.) — server-side; Tenji already benefits.
- `feat(web): play streams in the built-in browser player` (`f94f5eca`) — web-only; Tenji
  plays via **MPV**, has its own player path.
- `fix(web): don't open /events socket on splashscreen routes` — Denshi-specific double-connect.
- Integrations "Connect with AniList" token-paste modal — verify Tenji's login already covers
  AniList linking (it has the profile login screens); port only if the AniList-link UX is missing.

---

## 1. API generated layer — re-sync (do first)

Tenji's `src/api/generated/{types,endpoints,endpoint.types,hooks_template}.ts` are stale
(pre-Nakama-rooms, missing `UserDebridStatus`). These are framework-agnostic TS — copy
wholesale from the current `seanime-web/src/api/generated/` (after the codegen run already
done in seanime).

Brings in (verify present after copy):
- **Nakama rooms**: `Nakama_WatchRoom`, `Nakama_RoomCard`, `Nakama_RoomParticipant`,
  `Nakama_PoolUser`, `Nakama_PoolUserSource`, `Nakama_RoomPlaybackStatusPayload`, and the
  `API_ENDPOINTS.NAKAMA_ROOMS.*` group (`NakamaWatchRoomList/Create/Join/Leave/SetControl/
  ForceTracks/AutoSkip`) + their `*_Variables`.
- **Profile**: `UserDebridStatus`, `UserLoginResponse`, `Models_Theme.userId`, status fields
  (`serverHasUsers`, `serverAuthenticated`, `userDebrid`).

After copying, re-check Tenji's own `theme` handling for the `Models_Theme.userId` fallout
(seanime-web needed a `userId: 0` placeholder in its theme default — Tenji may have an
equivalent default to patch; grep Tenji for `Omit<Models_Theme` / theme defaults).

⚠️ Don't copy the seanime-web **hooks** files wholesale — Tenji's hooks use its own query
client. Hand-add new hooks per Tenji's existing pattern (§2).

---

## 2. Nakama watch rooms — the test goal

### 2.1 WS event constants
Add to Tenji's ws-events enum (find it near `src/api/components/websocket-event-router.ts`)
the 4 room events (must match the server strings exactly):
- `nakama-rooms-updated` (server→client: discovery list changed)
- `nakama-watch-room-state` (server→client: a room's state to its members)
- `nakama-room-playback-status` (client→server: report a control action)
- `nakama-room-playback-sync` (server→client: apply a controller's action)

### 2.2 Hooks
Add to Tenji's `src/api/hooks/nakama.hooks.ts` (mirror seanime-web's new hooks, Tenji style):
`useNakamaWatchRoomList`, `useNakamaCreateWatchRoom`, `useNakamaJoinWatchRoom`,
`useNakamaLeaveWatchRoom`, `useNakamaSetWatchRoomControl`,
`useNakamaSetWatchRoomForceTracks`, `useNakamaSetWatchRoomAutoSkip`. All hit
`API_ENDPOINTS.NAKAMA_ROOMS.*`.

### 2.3 Rooms UI (new — Tenji has none)
Port `seanime-web/.../_features/nakama/nakama-manager.tsx`'s `WatchRoomsSection` into a
Tenji screen/sheet using Tenji's RN component kit (not the web `Modal`/`Button`/`Switch`):
- **Discovery cards**: cover, room name, `Host: user`, show·episode, yellow lock (password),
  member count, Join. Inline create (name + optional password); inline join-password.
- **In-room**: member list + (host) control panel — per-member "can control" toggle +
  Everyone/Host-only; **force-tracks** switch; **autoskip vote** (3-way on/off/auto +
  `On · X on / Y off` display).
- Identity: "me" = participant whose `clientId` === this client's id (same approach as web).
- Lift current room to a Tenji atom (the web uses `currentWatchRoomAtom`) so the player layer
  can read it.
- Listen for `nakama-rooms-updated` → refetch list; `nakama-watch-room-state` → update current
  room.

### 2.4 Player sync — MPV wiring (the hard part)
seanime-web's `nakama-room-sync.ts` drives an HTMLVideoElement; Tenji must drive **MPV**.
Reimplement against the MPV ref + `onProgress`:
- **Emit** (`nakama-room-playback-status`) when allowed to control (host or granted), on
  play/pause/seek. MPV has no DOM "seeked" event — detect actions by intercepting the
  player-control handlers (the play/pause/seek buttons + gestures in
  `src/components/features/player/`) and/or diffing `onProgress` position jumps. Payload is
  **position + media id/episode only** — NO track fields (per-user tracks), except the host's
  audio/subtitle indices when force-tracks is on.
- **Apply** (`nakama-room-playback-sync`): call MPV `seekTo(currentTime)` when off by >~0.75s,
  `play()`/`pause()` to match. **Echo guard**: set an `applyingRemoteUntil` window (~800ms) so
  the seek/play you trigger doesn't re-broadcast.
- **Force-tracks**: when `room.forceHostTracks`, apply the host's audio/subtitle track via
  MPV's track-select API (find the MPV equivalent of `selectTrack`).
- **Autoskip**: Tenji has its own OP/ED skip logic (`player-auto-next.tsx` / player hooks).
  Apply the same room rule as web: **only the room controller auto-skips**; non-controllers
  follow the synced seek; on/off = `room.effectiveAutoSkip`. Report this client's vote via
  `useNakamaSetWatchRoomAutoSkip`.

### 2.5 Cross-platform notes
- The server is player-agnostic (relays position/play-pause). Denshi (codec-patched Chromium
  `<video>`) and Tenji (MPV) interoperate as long as both emit/apply the same payload.
- Stream source must resolve the same on both (debrid/torrent/onlinestream) — both hit the same
  server, so the same media id → same stream. Local-file rooms won't work cross-device.

---

## 3. Profile / per-user deltas (client-relevant)

Tenji already has login/gating/sign-out (`2cd9fe1`). Verify/port the rest of the per-user
surface the window added:
- **`UserDebridStatus`** (new type) → a per-user "use server debrid vs my own provider/key"
  setting screen, matching `a1007193 feat(profile): per-user debrid auto-select`. Port if Tenji
  exposes debrid settings.
- **Anon browse-only**: ensure Tenji handles the server's 403s on stream/torrent ops when not
  logged in (`f018cad4`, `2b602d6f`) — show "log in to stream" rather than a raw error.
- **Per-user status fields** (`serverHasUsers`, `serverAuthenticated`, `userDebrid`) — wire into
  Tenji's status/login gating if not already.

---

## 4. Suggested order

1. Re-sync the 4 generated files (§1) + patch theme `userId` fallout. **Verify Tenji
   typechecks** (`tsc`/expo).
2. WS event constants (§2.1) + hooks (§2.2).
3. Rooms UI screen (§2.3) — get discovery/create/join/members/control rendering against the
   live server (testable without playback).
4. MPV player sync (§2.4) — emit/apply + echo guard. **This is where cross-platform testing
   happens** (Denshi web/native ↔ Tenji MPV).
5. Autoskip vote + force-tracks wiring (§2.4).
6. Profile deltas (§3) as needed.

---

## 5. Open questions / risks

- **MPV action detection**: does `expo-mpv-player` emit discrete play/pause/seek events, or only
  `onProgress` ticks? If only ticks, control actions must be captured at the UI handler layer.
  Confirm the module's event surface before building emit.
- **MPV track-select API**: needed for force-tracks; confirm it exists (audio + subtitle index).
- **Tenji autoskip location**: locate Tenji's existing OP/ED auto-skip (player hooks) to add the
  room-controller-only rule — mirror seanime `video-core-time-range.tsx`.
- **Generated-file drift**: copying generated TS wholesale may pull web-only struct refs; if
  Tenji's tsc complains about unused/incompatible imports, reconcile surgically (memory note).
- **Nothing in seanime is committed/deployed yet** — the room backend must be deployed to the
  VPS before Tenji (or Denshi) can talk to it.
