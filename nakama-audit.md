# Nakama Watch Rooms — implementation audit

> Written 2026-06-23. Read-only audit (no code changed). Pairs with
> `nakama-watchroom-sync-handoff.md` (debugging handoff) and `nakama-room.md` (design spec).
> Goal: find errors, inconsistencies, and the real source of the current "follower never
> plays" problem from the **code**, and correct hypotheses that the static evidence rules out.

## STATUS — fixes implemented 2026-06-23 (built + typechecked; NOT yet deployed)

Implemented in this session (backend `go build ./...` clean, `go vet` clean, `nakama` +
`directstream` tests pass; web `tsgo` clean, 0 errors). Tenji unchanged — it benefits from the
backend fix and has no auto-wedge.

- **§2.1 PRIMARY — per-peer URL re-resolution.** Join-stream now shares the host's *selection*
  (`torrentItemId` + `fileId`) instead of the raw CDN link; each peer resolves its **own** fresh
  link from the already-added item (cheap — no search, no `createtorrent`), so peers never contend
  on one link. Fixes both TD (native) and DT (external/MPV). Raw-link fallback kept for
  direct-StreamUrl releases. Files: `debrid/client/stream.go` (`StreamManager.currentFileId`,
  `StartStreamOptions.SharedTorrentItemId`, shared-item branch in `startStream`),
  `debrid/client/repository.go` (`GetUserStreamShare` → `UserStreamShare{StreamUrl,Filepath,
  TorrentItemId,FileId}`), `handlers/nakama_rooms.go` (prefer shared selection).
- **§3.1 HIGH — web opt-out-on-abort wedge.** A failed/aborted open (no `<video>` ever mounted)
  is no longer treated as a user close, so it doesn't opt the follower out → recoverable
  (auto-follow + Join button both keep working). `nakama-room-sync.ts` (`everHadVideoRef`).
- **§4.1 MED — multi-source ticker echo.** Broadcast ticker now excludes the **actual last
  driver** (`lastControllerClientID`), not just `ControllerKey`, so a granted non-host driver
  isn't echoed its own position. `watch_room.go`.
- **§4.2 LOW — `resolveRelay` dead code** trimmed (`_, allowed := …`; dropped the discarded
  `targets`). `watch_room.go`.
- **§5.2 MED — playbackType clamp.** `getResolvedPlaybackType()` coerces `"default"` →
  `"nativeplayer"` so an Electron follower can't launch mpv on the headless server.
  `handle-debrid-stream.ts`.

Deliberately NOT changed: §3.2 auto-latch-retry (would hammer; the §3.1 fix + Join button cover
recovery), §5.1 host-leave-closes-room (intended UX), §5.3 diagnostics (handoff wants them kept
for the live test). The §2.1 root cause is still worth the one-curl confirmation in §2.1 below —
but the fix is robust regardless (it makes join-stream converge on the known-working normal-play
resolution path).

---

## 0. Method / what was traced

Full read of the same-instance room system end to end:

- Backend: `internal/nakama/watch_room.go`, `nakama.go` (dispatch), `internal/handlers/nakama_rooms.go`,
  `identity.go`, `websocket.go`; debrid `stream.go` + `repository.go`; directstream
  `stream.go` / `debridstream.go` / `httpstream.go`; nativeplayer `events.go` / `nativeplayer.go`;
  events `scoped.go` / `websocket.go`; core `session.go` / `modules.go`.
- Web: `nakama-room-sync.ts`, `native-player.tsx`, `video-core.tsx`, `handle-debrid-stream.ts`.
- Tenji: `watch-room.ts`, `use-watch-room-sync.ts`, `session.ts`, `_layout.tsx`,
  `websocket-provider.tsx`.

---

## 1. TL;DR — corrected mental model

**The handoff's leading hypothesis (§5, Denshi bullet) is wrong and should not be chased.**
It guessed the join-stream path "likely does not complete the native-player open handshake,
so `IsOpenActive` is false and the VideoCore never mounts." The code says otherwise:

- The follower's join-stream and a normal auto-select **call the exact same function**
  (`debrid_client/StreamManager.startStream`), which **unconditionally** runs the native-player
  open handshake (`s.ds(opts).BeginOpen(...)`) for `PlaybackType == nativeplayer`
  (`stream.go:267-271`). `BeginOpen` → `updateOpenStepLocked` → `nativePlayer.OpenAndAwait(clientId)`
  and sets `preparingClientID = clientId`, so `IsOpenActive(clientId)` returns **true immediately**
  (`directstream/stream.go:120-133, 218-231`).
- The smoking-gun TD log confirms the follower **did** receive `open-and-await` — its player
  opened (`active=true`, "Loading…"). The handshake is **not** the gap.

**The real gap is one step later: the `"watch"` event never fires.** The follower's `<video>`
(and therefore `vc_videoElement`) mounts **only** when the native player receives `"watch"`
with a `playbackInfo.streamUrl` — gated in `video-core.tsx:408`:

```tsx
{(!!state.playbackInfo?.streamUrl && !state.loadingState) ? ( …<video ref={combineRef}/>… )}
```

`open-and-await` sets `loadingState=<step>, playbackInfo=null` (gate **false**); only `"watch"`
sets `playbackInfo=<info>, loadingState=null` (gate **true**). So "`video:false` the entire test"
== **"watch" was never delivered with a valid stream URL.**

Server-side, `"watch"` is the **last** line of `directstream.loadStream` (`stream.go:428`),
reached only after `LoadContentType()` **and** `LoadPlaybackInfo()` succeed — both of which
**fetch the shared raw debrid URL from the VPS** (`httpstream.go:119-143` content-type/length;
`:228-256` MKV metadata parse). If either hangs or fails on the shared URL, `"watch"` never
fires → no `<video>` → the sync layer has nothing to drive. **All three reported symptoms
collapse into "the shared URL does not load server-side for a second consumer."**

This *matches* the handoff's own §5.3 ("verify the shared TorBox URL plays on a second client")
— that is the thing to test, and it is the leading root cause, not the open handshake.

---

## 2. Root-cause analysis

### 2.1 [PRIMARY] Shared raw debrid URL fails/hangs on the second consumer → no `"watch"`

**Path (TD, Denshi follower / native player):**
`apply NAKAMA_ROOM_PLAYBACK_SYNC` → `maybeAutoStart` → `joinRoomStream.mutate(...)`
(`nakama-room-sync.ts:122-127`) → `HandleNakamaWatchRoomJoinStream` →
`GetUserStreamShare(controllerUserID)` returns the host's `currentStreamUrl` verbatim
(`repository.go:380-391`) → `StartStream` with `Torrent={StreamUrl:url}` →
`startStream` takes the `selectedTorrent.StreamUrl != ""` branch (`stream.go:334-337`),
skips `AddTorrent`, → `PlayDebridStream` → `loadStream` → **`LoadContentType` + MKV parse on
the shared URL** → (success) `nativePlayer.Watch`.

**Why it's the differentiator vs. a normal (working) auto-select on the same follower:**
a normal start resolves a **fresh** CDN link for the follower; join-stream **reuses the host's
link while the host is actively streaming it**. The only thing different about the broken path
is *contention on one URL*. Leading mechanism: the debrid CDN (TorBox) link is IP-bound or
concurrency-limited, so the VPS's second fetch (for the follower) hangs or is refused while the
host streams the same link.

- Hang → player stuck on "Loading metadata…", `video:false`, **no error** (matches "both
  players open, nothing plays"). `parser.GetMetadata(metadataCtx)` uses `manager.playbackCtx`
  with no independent deadline (`httpstream.go:240-251`), so a stalled CDN read blocks
  `loadStream` indefinitely.
- Clean failure → `LoadContentType` returns `""` → `preStreamError` → `AbortOpen` →
  `"abort-open"` → player closes (which then triggers the opt-out wedge, §3.1).

**Single global debrid account (reframes the design rationale).** `Repository.provider` is a
**single** `mo.Option[debrid.Provider]` set from global settings (`repository.go:35,139-167`).
Per-user sessions split directstream/playback/events, **not** the debrid account. So on the
same-instance pool there is exactly **one** TorBox account; host and follower both stream the
**same account/link concurrently**. The handoff's "works across different TorBox accounts"
benefit only applies to (not-yet-wired) external-instance peers; for current testing it's one
account, which makes per-link concurrency the prime suspect.

**Verify (cheap, decisive):** while the host streams, from the VPS
`curl -sI '<host CDN url>'` and `curl -r 0-1000000 '<host CDN url>' -o /dev/null` — if it hangs,
429s, or 403s, confirmed. (Get the URL from `GetUserStreamShare` / the `relay` log media.)

**Likely fix direction (NOT implemented):** share the *selection* (the already-added
`torrentItemId` + `fileId`), not the raw URL, and have each peer cheaply re-resolve **its own**
CDN link from that item (no `createtorrent`, so the selection-reuse win is kept). This dodges
single-link contention. Today `GetUserStreamShare` returns only `(streamUrl, filepath)` —
it would need to also return `torrentItemId`/`fileId`.

### 2.2 [PRIMARY] DT (Tenji follower / external player) never opens — narrowed, not yet pinned

Static checks **rule out** the obvious suspects, so this needs the handoff's instrumentation:

- The `external-player-open-url` listener **is** mounted app-wide (`app/(app)/_layout.tsx:22`
  `usePlayerEventListener`), and on receipt it navigates to the player
  (`session.ts:639-747`). So a missing listener is **not** the cause.
- Tenji's WS registers `?session=<token>` **and** `?clientId=` (`websocket-provider.tsx:49-63`),
  so the conn is tagged with the user id and the `clientId` matches `getClientId()` — per-user
  routing of `external-player-open-url` to the follower should work
  (`SendEventToIfOwner`, `websocket.go:233-246`).

Remaining suspects for "never opens", in order:
1. **Same root as §2.1**: for external-player there is *no server-side metadata parse* — the
   server just emits the URL and MPV on the phone fetches it. If `GetUserStreamShare` returns
   `ok=false` (host link not in memory yet), join-stream **falls back to `AutoSelect=true`**
   (`nakama_rooms.go:243-255`) → the server resolves a fresh link for the Tenji user → that
   path *does* emit `external-player-open-url`. So "never opens" implies the **emit/HTTP call
   itself didn't happen or errored**, not the URL.
2. `joinStream` mutation erroring server-side (e.g. `StreamInfo.Active=false` → "room has no
   active stream", `nakama_rooms.go:224-227`) because the room's `PlaybackActive`/`CurrentMediaInfo`
   weren't set yet when the follower reacted. `PlaybackActive` is only set when the controller's
   first non-stopped status is relayed (`watch_room.go:561-575`); a follower that reacts to the
   *first* sync could race ahead of that on a reconnect edge.
3. A `maybeFollow` early-return (`watch-room.ts:130-166`): `driverGuard`, `optedOutRoomId`,
   or `followedKeyRef` already latched (see §3.2).

Instrument: the `follow: START debrid join-stream` `roomDebug` line already exists
(`watch-room.ts:157`); confirm it appears, then confirm the server logs a join-stream + an
`external-player-open-url` emit for the Tenji clientId.

---

## 3. Compounding bugs (turn a transient failure into a stuck state)

### 3.1 [HIGH] Web opt-out-on-abort wedge — `nakama-room-sync.ts:164-187`

The "player closed" effect treats **any** `active: true→false` as a user close:

```ts
if (!was || playerActive) return            // only true -> false
…
} else if (room.playbackActive) {
    setOptedOut(room.id)                    // follower "closed" => stop auto-reopen
}
```

But a **server-side `AbortOpen`** (e.g. the shared URL failed in §2.1) also flips
`active: true→false`. The follower then opts out of the room stream, and `maybeAutoStart`
permanently bails (`if (optedOutRoomId === p.roomId) return`, `:138`). The only recovery is the
manual "Join room stream" button — which re-runs the **same** failing path → aborts → opts out
again. A one-time failure becomes "never plays for the rest of the session." This plausibly
explains "`video:false` for the **entire** 12-minute test." There is no distinction between
"user closed the player" and "server aborted the open."

### 3.2 [MED] Auto-start latch never clears on failure — web `:113-119`, Tenji `:128,151-153`

`autoStartingKeyRef` (web) / `followedKeyRef` (Tenji) latch the `mediaId:ep:type` key on first
attempt and only clear when `playingThis`/`activeSource` matches or a **new** key arrives. If the
start fails (player never mounts — the actual bug), the key stays latched and the follower
**never retries** the same episode. Marked as a deliberate ponytail tradeoff in the handoff, but
combined with §3.1 it means the follower cannot self-recover even after the cause clears.

---

## 4. Multi-source-control inconsistencies (latent; bite once control is granted to non-host)

### 4.1 [MED] Heartbeat ticker excludes the *controller*, discrete relay excludes the *sender*

- `runBroadcastLoop` fans the authoritative position to everyone **except `room.ControllerKey`'s
  client** (`watch_room.go:226-241`).
- `RelayPlaybackStatus` broadcasts a discrete action to everyone **except the sender**
  (`:588-593`).

These two exclusion bases diverge as soon as a **non-host member with `CanControl` drives**.
That member is *not* `ControllerKey`, so the 1 s ticker echoes the server's position **back to
them**, and their apply guard does **not** protect them: the guard is
`if (canControl && amController) return` (`nakama-room-sync.ts:283`, `use-watch-room-sync.ts:200`)
— it only shields the single `amController`. A non-controller driver therefore reconciles to the
echo of its own heartbeat → position/seek jitter. The code conflates "is the controller" with
"is currently driving"; with `SetControl(all=true)` the latter is many-valued.

### 4.2 [LOW] `resolveRelay` is dead weight — `watch_room.go:542-554, 657-680`

`RelayPlaybackStatus` calls `room.resolveRelay(senderClientID)` for `(targets, allowed)` but
**discards `targets`** (`_ = targets`) and recomputes the fan-out inline under its own lock.
`resolveRelay` does a redundant `RLock` + full participant scan; only its `allowed` bool is used.
Either use its `targets` or reduce it to a permission predicate.

---

## 5. Spec ↔ implementation inconsistencies

### 5.1 [MED] "Stream never stops when the host disconnects" only half-holds

`nakama-room.md` §2.6 promises the stream survives host departure with auto-promotion. The code
splits two cases that the spec treats as one:

- Host **client disconnect** (ws drop): `HandleClientDisconnect` keeps the room and promotes
  control by join order (`watch_room.go:416-429`). ✓ matches spec.
- Host **explicit Leave**: `LeaveRoom` with the host key **tears the whole room down** and sends
  `NakamaWatchRoomClosed` to everyone (`:371-389`). ✗ the spec's "stream never stops" does not
  hold for an intentional leave.

Also, a promoted-away host that never returns **lingers as a ghost participant** (kept for
reconnect); it's only cleaned when the room empties. Benign (stale `ClientID` sends are no-ops)
but unbounded for a long-lived room.

### 5.2 [MED] `getResolvedPlaybackType()` can return `"default"` for an Electron follower

`handle-debrid-stream.ts:56-76`: if `__isElectronDesktop__` but the device's playback method is
**not** NativePlayer, `getPlaybackType()` returns `"default"`. The watch-room join then requests
`playbackType:"default"`, which on the server means "launch mpv" — **on the headless VPS**
(`stream.go:618-677`, `StartStreamingUsingMediaPlayer`). Not hit when the user runs Denshi in
native-player mode (so not the current bug), but it's a latent footgun: a follower on a non-native
Electron config silently drives mpv on the server instead of opening a player.

### 5.3 [LOW] Temporary diagnostic code shipped in the committed baseline

Per-apply `roomDebug`/`NAKAMA_ROOM_DEBUG` lines are live in both clients
(`nakama-room-sync.ts:304-309`, `use-watch-room-sync.ts:221-223`) and the server logs them at
Debug (`nakama.go:339-343`). Fine for the current debugging push (the handoff *wants* them), but
they are "temporary" instrumentation in committed source and should be stripped before release.
Note the handoff also asks for the **top-of-apply gate probe** (the `gates{video,…}` line) to be
re-added — it was reverted and is the single most useful probe.

---

## 6. Confirmed FINE — rule-outs (so the next session doesn't re-chase them)

- **`dataUserID` vs `CurrentUserID`**: identical for a logged-in user on the networked VPS
  (`identity.go:140-179`); the admin-fallback diverges only on a password-less local install.
  No user-id mismatch between the room identity (`CurrentUserID`) and the stream (`dataUserID`).
- **Per-user event routing**: `SendEventToIfOwner(clientId, ownerUserID)` delivers when the
  conn's `UserID == ownerUserID` **or `0`** (`websocket.go:233-246`). A correctly-tagged follower
  receives its own native-player events. (Both web and Tenji tag the conn via `?session=`.)
- **Native-player open handshake**: identical for auto-select and join-stream; `open-and-await`
  *does* reach the follower (the player opens). Not the gap (see §1).
- **Tenji apply logic is symmetric, not inverted**: `if (p.paused && !state.paused) pause()` /
  `else if (!p.paused && state.paused) play()` (`use-watch-room-sync.ts:214-220`). The reported
  "play/pause inverted" is a red herring (as the handoff concluded) — there is no live `<video>`
  to apply to.
- **Tenji cross-screen reactions are mounted app-wide** (`app/(app)/_layout.tsx:16-22`):
  `useWatchRoomLiveState`, `useWatchRoomFollow`, `usePlayerEventListener`.
- **Sender exclusion / authoritative state**: the relay correctly updates server state and never
  echoes the sender (`watch_room.go:559-613`); this did kill the controller oscillation.
- **Auto-skip vote tally** (`recomputeAutoSkipLocked`, `:486-499`): correct (strict majority,
  tie = off); unit-tested.

---

## 7. Recommended next diagnostics (sharpened from the handoff)

1. **Confirm §2.1 first (one curl, decisive):** while the host streams, fetch the host's CDN URL
   from the VPS. Hang/429/403 ⇒ shared-raw-URL contention is the root cause; pivot to per-peer
   re-resolve from a shared `torrentItemId` (do **not** keep chasing the open handshake).
2. If the URL *does* serve a second consumer cleanly, then add back the **top-of-apply gate
   probe** and watch for whether `"watch"` arrives at the follower (server log
   `directstream: Signaling native player that stream is ready` at `stream.go:427` immediately
   precedes `nativePlayer.Watch`). Absence ⇒ `loadStream` died in `LoadContentType`/metadata.
3. **Decouple the opt-out wedge (§3.1)** before any more live runs — otherwise one failed open
   poisons the rest of the session and every subsequent observation is of the wedged state, not
   the original bug.
4. DT: confirm the `follow: START …` line + a server `external-player-open-url` emit for the
   Tenji clientId; if the emit is present but nothing opens, it's MPV failing on the shared URL
   (same §2.1 root); if the emit is absent, it's the join-stream call/guard.

---

## 8. File:line index (most-touched)

**Backend**
- `internal/nakama/watch_room.go` — hub, `RelayPlaybackStatus` `:542`, ticker `:208`,
  `StreamInfo` `:629`, `resolveRelay` `:657`, leave/disconnect/promote `:354/:416/:734`.
- `internal/handlers/nakama_rooms.go` — `HandleNakamaWatchRoomJoinStream` `:209`, share/fallback `:243`.
- `internal/debrid/client/stream.go` — `startStream` `:206`, native open `:267`, StreamUrl branch
  `:334`, `IsOpenActive` gates `:281/:370/:481/:700`, externalPlayer emit `:679`.
- `internal/debrid/client/repository.go` — `GetUserStreamShare` `:380` (URL+filepath only; no
  torrentItemId), single global `provider` `:35/:139`.
- `internal/directstream/stream.go` — `BeginOpen`/`IsOpenActive` `:87/:120`, `loadStream`→`Watch`
  `:359/:428`.
- `internal/directstream/httpstream.go` — `LoadContentType` `:119`, metadata parse `:228`,
  client `StreamUrl` = VPS proxy `:202`.
- `internal/events/websocket.go` — `SendEventToIfOwner` `:233`; `scoped.go` `:22`.
- `internal/handlers/websocket.go` — conn user tagging via `?session=` `:71-75`; disconnect hook `:95`.

**Web (`seanime-web`)**
- `nakama-room-sync.ts` — `maybeAutoStart`/`startRoomStream` `:117-153`, opt-out wedge `:164-187`,
  apply `:260-325`.
- `native-player.tsx` — `open-and-await`/`watch` handling `:113-191`.
- `video-core.tsx` — `<video>` mount gate `:408`, `setVideoElement` `:1000`.
- `handle-debrid-stream.ts` — `getPlaybackType` `:56-76`.

**Tenji (`seanime-tenji`)**
- `src/lib/nakama/watch-room.ts` — `useWatchRoomFollow`/`maybeFollow` `:111-198`, join button `:203`.
- `src/lib/nakama/use-watch-room-sync.ts` — apply `:193-234` (symmetric/correct).
- `src/lib/player/session.ts` — `external-player-open-url` `:639-747`.
- `app/(app)/_layout.tsx` — app-wide mounts `:16-22`.
- `src/api/components/websocket-provider.tsx` — WS url (`session`+`clientId`) `:49-63`.
