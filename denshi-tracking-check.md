# Denshi external-server: native-player tracking — what to verify

Context: Denshi now points at the VPS (`serverUrl` in denshi-settings). Playback-start was
fixed by a clientId reconnect fix in `websocket-provider.tsx`. This file tracks what still
needs confirming about **progress tracking** for the built-in (native / videocore) player.

## Key distinction (not a bug)

- **"Playing externally"** is the `playbackmanager` path — used by external OS players (mpv/vlc)
  and by **Tenji's libmpv**. It broadcasts a cross-client "playing externally" status.
- **Denshi's native player is `videocore`** — a separate path. It tracks progress itself in
  `internal/videocore/effects.go` but does **not** emit a "playing externally" status.
  → The missing "playing externally" label in Denshi is **expected**, not a regression.

## What the native player SHOULD do (server-side, on the VPS)

Driven by `videocore/effects.go`, gated by the clientId filter at `videocore.go:880`
(`eventClientID == currentState.ClientId`):

- `VideoStatusEvent` (periodic `video-status` from client) → `continuityManager.UpdateWatchHistoryItem`
  → **resume / continue-watching position**.
- `VideoCompletedEvent` → `UpdateEntryProgress` → **AniList progress** (only if
  Settings → Library → *Auto update progress* is ON).

## To verify (after installing the reconnect-fix build)

- [ ] Watch an episode in Denshi for a bit, close it, reopen the entry → does the
      **continue-watching / resume position** reflect where you stopped?
- [ ] Finish (or pass the threshold of) an episode → does **AniList progress increment**?
      (Confirm *Auto update progress* is enabled first.)
- [ ] Does playback-start now work **without** a manual reload? (validates the reconnect fix)
- [ ] On the VPS, tail the server log during a Denshi play and grep:
      `video-status`, `UpdateWatchHistoryItem`, `Failed to update progress`.
      - `video-status` arriving + history updating → tracking works (only the by-design
        "playing externally" label is absent).
      - `video-status` arriving but **skipped** → still a clientId mismatch
        (`videocore.go:880`); reconnect fix didn't fully stabilize identity.
      - no `video-status` at all → client isn't sending / ws not delivering.

## If tracking still fails after the fix

- Suspect residual clientId drift on mid-playback ws reconnect (orphaned
  `currentState.ClientId`, the #814 class) — `currentState.ClientId` is pinned at play-start
  and won't follow a clientId change. Possible follow-up: update the playback owner's
  clientId on reconnect, or relax the `videocore.go:880` filter when the proof is valid.
