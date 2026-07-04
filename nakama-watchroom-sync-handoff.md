# Nakama Watch Rooms — cross-platform sync handoff (start-from-scratch)

> Written 2026-06-24 after a long debugging session. The goal of this doc is to let a **new session start fresh** with all the hard-won findings, without re-deriving them. **Keep the shared-debrid-link design** (user requirement) — do not "fix" it by reverting to per-peer auto-select.

---

## 1. What the feature is

Nakama **same-instance "watch rooms"**: a pool of local users on one Seanime backend create/join rooms and watch an episode **in sync** (position + play/pause). It must work **cross-platform** between:

- **Denshi** — Electron desktop client. Serves a web bundle (`web-denshi`, built from `seanime-web`). Playback uses the **native player** (`native-player.tsx`, a `<VideoCore>` wrapping an `<video>`). `playbackType=nativeplayer`.
- **Tenji** — React-Native/Expo iOS client at `H:/Projects/seanime-tenji` (fork `ClinShaiju/seanime-tenji`). Playback uses **MPV**. For debrid it consumes `external-player-open-url` and navigates to its player. `playbackType=externalPlayerLink`.

Both talk to the **same Go backend** (VPS `opc@170.9.228.45` → https://seanime.clinshaiju.dev).

## 2. Current architecture (3 deliberate decisions — keep them)

1. **Server-authoritative sync.** `WatchRoom` holds `{PlaybackActive, paused, position, positionAt}`. The controller reports actions; the server updates state and broadcasts the **server-computed** state to all members (1s hub ticker + immediate on discrete actions). The server **excludes the sender** from discrete broadcasts and **excludes the controller** from the ticker (this killed an oscillation bug — see §4).
2. **Opt-in stream join.** Auto-open only when the host *starts while you're present*; **leaving stays left**; a **"Join room stream"** button re-joins. Client-local `optedOutStreamRoomIdAtom`.
3. **Shared debrid link.** Peers reuse the host's already-resolved TorBox CDN link verbatim — no per-peer re-selection (works across different TorBox accounts / external peers). Endpoint `POST /api/v1/nakama/watch-room/join-stream` → `GetUserStreamShare(controllerUserID)` → `StartStream` with a synthetic torrent carrying `StreamUrl` (server's pre-resolved path in `stream.go`).

## 3. The bug (what the user observes)

- **TD (Tenji host → Denshi follower):** both players open; play/pause **inverted** in earlier runs, **did nothing** in the last run; **seek does nothing**.
- **DT (Denshi host → Tenji follower):** the Tenji follower's player **never opens** (and never has).

## 4. THE SMOKING GUN (most important section)

We added client→server debug logging (iOS has no devtools) and watched the VPS live during a real TD test. Result:

```
relay sender=<tenji host> controller=true paused=false t=287.8 ... -> 1 follower(s)
CLIENT-DEBUG [<denshi follower>] RECV{paused:false,t:287.8} gates{video:FALSE, canCtrl:false, amCtrl:false}
```

- The Denshi follower **received every sync** but **`video:false` for the ENTIRE test** — `vc_videoElement` was null the whole time. The apply bails at `if (!videoElement) return`, so **nothing is ever applied**. **Zero `video:true` events** in 12+ minutes.
- Independently, the Denshi client **never emitted a play/pause** in any role — only `stopped=true`. (The emit listens to `vc_videoElement` events; no element ⇒ no emit.)
- The **host's emits were verified correct** in the relay log: `paused=true` when paused, `paused=false` with advancing `t` while playing. So the "inversion" **could not be reproduced server-side** — it's a red herring (or an artifact of the follower never having a live player). **Do not chase the inversion until the follower actually plays.**
- The follower **did** call `/nakama/watch-room/join-stream` repeatedly; the server logged `debridstream > Stream started` / `Stream is ready` (`playbackType=nativeplayer`) — **but the player never actually played it** (no video element ever appeared).

**Conclusion: the bug is "the follower's player never actually plays the shared link," not a sync-apply bug.** All three symptoms collapse into this. The sync layer (apply/emit) is almost certainly fine once there's a live video element.

## 5. Root-cause hypothesis + where to look

The shared `join-stream` starts the stream **server-side** but the **client player never genuinely plays it**:

- **Denshi (nativeplayer):** the native player opens via the server's `open-and-await` `NATIVE_PLAYER` event (`native-player.tsx` ~line 119 sets `state.active=true`), and only then does its `<VideoCore>` mount a `<video>` and set `vc_videoElement`. Critically, `StartStream` for `nativeplayer` **early-returns if `!IsOpenActive(clientId)`** (`internal/debrid/client/stream.go` ~line 370: `SendEvent(HideIndefiniteLoader); return nil`). The normal **auto-select** path completes the native-player open handshake first; the **join-stream** path likely does not, so the VideoCore never mounts ⇒ `vc_videoElement` stays null ⇒ `video:false`. **Compare the two start paths and make join-stream do whatever auto-select does to actually open + play the native player.** (Note: the native player *does* use `vc_videoElement` via its `<VideoCore>` — confirmed — so when it genuinely plays, the atom is set. It just never genuinely played.)
- **Tenji (externalPlayerLink):** join-stream → server emits `external-player-open-url` → `session.ts` should `router.push("/(app)/(media)/player")`. In DT this never opens. Check: does the `external-player-open-url` event actually reach the Tenji follower's socket, and does `session.ts` navigate? (`seanime-tenji/src/lib/player/session.ts`, the `external-player-open-url` handler ~line 700-747.)
- **The shared URL itself:** verify the host's TorBox CDN link is actually playable by a *different* client (curl / paste into a player). If TorBox binds the link to the host's session/IP, the verbatim share won't play and the design needs a per-peer "unrestrict with shared infohash" instead of a raw URL share. **Rule this out early.**

## 6. Diagnostic infrastructure (committed, reusable)

- **`nakama-room-debug` WS event** (client→server), logged server-side as `nakama/rooms CLIENT-DEBUG [<clientId>] <msg>`.
  - Backend handler: `internal/nakama/nakama.go` (in the `eventListener.Channel` loop). Const: `internal/events/events.go` `NakamaRoomDebug`.
  - Web const: `seanime-web/.../ws-events.ts` `NAKAMA_ROOM_DEBUG`. Tenji: `watch-room.ts` `NAKAMA_ROOM_EVENTS.ROOM_DEBUG` + **`useRoomDebug()`** helper.
- **Apply logging** (committed, bottom of each apply): `apply recv{paused,t,hb} local{paused,t} action=[seek/pause/play/none]` — web `nakama-room-sync.ts`, tenji `use-watch-room-sync.ts`.
- **Player lifecycle** (Tenji, committed): `PLAYER mounted/unmounted`, `PLAYER terminate-signal fired`, `follow: START/STOP`, `ROOM CLOSED`.
- **⚠ The single most useful probe — the top-of-apply gate log — was REVERTED from source** when restoring the shared-link baseline. Re-add it (one block right after `if (!p) return` in both apply hooks) to reproduce the `video:false` finding:
  ```ts
  if (!p.heartbeat) roomDebug(`RECV{paused:${p.paused},t:${(p.currentTime ?? 0).toFixed(1)},stop:${!!p.stopped}} `
      + `gates{video:${!!videoElement},canCtrl:${canControl},amCtrl:${amController}}`)   // web: sendMessage({type: NAKAMA_ROOM_DEBUG, payload: ...})
  ```
  It's **still present in the deployed Denshi-22:56 + the last Tenji EAS build**, so you can read it from the live log immediately without rebuilding.
- **Read the log:**
  ```bash
  ssh -i ~/.ssh/seanime_oracle.key -o IdentitiesOnly=yes opc@170.9.228.45 \
    'sudo journalctl -u seanime --since "6 min ago" --no-pager | grep -iE "CLIENT-DEBUG|relay sender|join-stream|debridstream >"'
  ```
  Relay line format: `relay sender=<clientId> controller=<bool> paused=<bool> t=<pos> stopped=<bool> media=<id> ep=<n> -> N follower(s)`.
- A persistent **Monitor** on `... journalctl -f | grep -E 'CLIENT-DEBUG|relay sender'` streams events live as the user tests (very effective; filter tightly or it floods).

## 7. EAS Update for Tenji (fast iteration — USE THIS, no 15-min IPA rebuild)

The installed IPA is wired for OTA: project **`112362af-08e4-4daa-a481-fc214e9fe092`**, channel **`stable`**, `runtimeVersion` policy `appVersion` = **`0.1.21`** (`app.config.ts`).

```bash
cd seanime-tenji
EXPO_TOKEN=<token> npx --yes eas-cli@latest update --channel stable --environment production \
  --message "..." --non-interactive
```
- Account is **cvslinc** (clinshaiju@hotmail.com). `eas-cli` isn't installed locally — use `npx eas-cli@latest`.
- **OTA applies on the SECOND launch** (downloads on 1st launch, swaps in on 2nd). Tell the user: open → wait ~10s → force-close → reopen.
- **Do not store the token in a file** — ask the user for `EXPO_TOKEN` each session.
- Full IPA (only when native changes): `gh workflow run build-ios.yml --repo ClinShaiju/seanime-tenji --ref main` → unsigned IPA as a GitHub Release.

## 8. Deployed state / version gotchas

- **VPS backend**: has the exclude-sender fix + the `CLIENT-DEBUG` handler (commit `c97ce64a` deployed). The exclude-sender fix **did** kill the controller play/pause oscillation (confirmed in logs).
- **Denshi**: the **instrumented** build is the **local** `seanime-denshi/dist/seanime-denshi-3.8.8_Windows_x64.exe` (≈22:56). **The auto-update RELEASE v3.8.8 was CI-built from `1180341c` — BEFORE the diagnostics — so auto-update does NOT give the instrumented build.** Same version number, so installing the local .exe won't be fought by the updater. **Always have the user manually install the local .exe**, and confirm it's live by the presence of `CLIENT-DEBUG` lines.
- **Tenji**: last EAS push was group `38822300` (had the now-reverted top-RECV gate log). Committed baseline is `89a3dac`. Re-push after any source change.
- Version: seanime **3.8.8 "Kanata"**.

## 9. Key files

**Backend**
- `internal/nakama/watch_room.go` — `WatchRoom` authoritative state, `RelayPlaybackStatus` (sender-exclusion + broadcast), hub `runBroadcastLoop` ticker, `StreamInfo`, `LeaveRoom`.
- `internal/nakama/nakama.go` — client-event loop; `NakamaRoomPlaybackStatus` relay + `NakamaRoomDebug` log.
- `internal/handlers/nakama_rooms.go` — `HandleNakamaWatchRoomJoinStream` (shared-link join). `routes.go` — the route.
- `internal/debrid/client/repository.go` — `GetUserStreamShare(userID)`.
- `internal/debrid/client/stream.go` — **~line 334**: pre-resolved `StreamUrl` path (skips AddTorrent). **~line 370**: `nativeplayer && !IsOpenActive ⇒ return nil` (the likely culprit for Denshi follower not opening).

**Web (`seanime-web`)**
- `src/app/(main)/_features/nakama/nakama-room-sync.ts` — sync hook (`useWatchRoomPlayerSync`): apply, `startRoomStream`/`maybeAutoStart`, `useRoomStreamJoin`. Uses `vc_videoElement`.
- `src/app/(main)/_features/nakama/nakama-manager.tsx` — modal, atoms (`optedOutStreamRoomIdAtom`, `currentWatchRoomAtom`), join button.
- `src/app/(main)/_features/video-core/video-core.tsx` — overlay "Join room stream" button, `isWatchRoomFollower`.
- `src/app/(main)/_features/native-player/native-player.tsx` — `open-and-await` → `active=true` → `<VideoCore>` → sets `vc_videoElement`. **This is where the follower's player should come alive.**

**Tenji (`seanime-tenji`)**
- `src/lib/nakama/watch-room.ts` — `useWatchRoomFollow`/`maybeFollow` (debrid → `joinStream`, torrent → entry-nav), `useRoomDebug`, opt-out, reconnect re-join.
- `src/lib/nakama/use-watch-room-sync.ts` — apply.
- `app/(app)/(media)/player.tsx` — terminate-signal effect, `handleBack`, lifecycle debug.
- `src/lib/player/session.ts` — `external-player-open-url` → `router.push` player (**DT-not-opening lives here or upstream of it**).

## 10. Build / deploy runbook (condensed; full version in `CLAUDE.md`)

- Portable Go: `export GOROOT="$HOME/go-sdk/go" PATH="$GOROOT/bin:$PATH"` (`go1.26.2`, GOPATH `C:\Users\Clin\go`). `go build/vet/test ./pkg/...` work natively.
- Backend arm64: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o seanime-server-linux-arm64 .`
- Deploy: scp to `:/home/opc/seanime/seanime.new`, stop service, `mv` over `seanime`, **`sudo chcon -t bin_t seanime`** (REQUIRED or systemd 203/EXEC), start, verify `curl … → 200`. SSH: `-i ~/.ssh/seanime_oracle.key -o IdentitiesOnly=yes`.
- Denshi: `cd seanime-web && npm run build:denshi` → from **repo root** `rm -rf seanime-denshi/web-denshi && mv seanime-web/out-denshi seanime-denshi/web-denshi` → `cd seanime-denshi && npm run build:win`. **⚠ The Bash tool's cwd resets to repo root between calls — run the swap with absolute repo-root paths or it fails silently.**
- Typecheck: web `npx tsgo --pretty false`; Tenji `npx tsc --noEmit`. Both were clean this session.
- If using `rtk` prefix for verbose builds, note `build:win` exit code may be misreported when chained after a `cd` — verify the `dist/*.exe` timestamp instead.

## 11. Suggested first moves for the new session

1. Confirm the finding fast: read the live log while the user does one TD round (Denshi follower) — you should see `RECV … gates{video:false}` (deployed builds still log it). If you rebuilt, re-add the top-of-apply gate probe first.
2. **Make the shared-link follower actually PLAY** — the whole ballgame. Diff the **auto-select** start path vs the **join-stream** start path for `nativeplayer`: what does auto-select do to satisfy `IsOpenActive` / trigger `open-and-await` that join-stream skips? Likely the client must open the native player (handshake) *before/around* the join-stream call, or `HandleNakamaWatchRoomJoinStream` must drive the same open sequence as a normal debrid start.
3. **Independently verify the shared TorBox URL plays on a second client** (curl/manual). If it doesn't, the share must carry an infohash/fileId the peer unrestricts with its own account, not a raw URL.
4. **Tenji DT**: instrument/trace `external-player-open-url` reaching the Tenji follower and `session.ts` navigating. The `PLAYER mounted` debug + `follow: START` will show whether it even tries.
5. Only after a follower reliably **plays**, retest play/pause/seek. The host emit is verified correct and the apply is state-based — expect it to "just work" once `vc_videoElement` is non-null.

## 12. Memory / context pointers

- User memory: `~/.claude/projects/H--Projects-seanime/memory/` (see `MEMORY.md`). Relevant: `stream-selection-quality-over-speed`, `debrid-preload-prewarm`, `directstream-audio-switch-gap`, `nakama-rooms-pool`, `tenji-ios-client`, `seanime-vps-deploy`, `surfshark-blocks-ssh-deploy`.
- Remotes: `origin`=upstream (no push), `fork`=`ClinShaiju/seanime` (push here). Work on `main`.
- If a deploy scp hangs with "Connection reset": Surfshark VPN is mangling SSH — disconnect it in the tray.
