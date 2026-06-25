import { Nakama_RoomPlaybackStatusPayload, Nakama_WatchPartyStreamType, NativePlayer_StreamType } from "@/api/generated/types"
import { nativePlayer_stateAtom, nativePlayer_terminateRequestedAtom } from "@/app/(main)/_features/native-player/native-player.atoms"
import { vc_audioManager, vc_mediaCaptionsManager, vc_subtitleManager } from "@/app/(main)/_features/video-core/video-core"
// Use the GLOBAL mirrors, not vc_videoElement / vc_lastKnownProgress directly: those are scoped to
// VideoCoreProvider (jotai-scope) and this hook runs app-wide in NakamaManager, OUTSIDE that scope,
// so the scoped atoms always read null here. VideoCoreGlobalBridge mirrors the active player into these.
import { vc_globalLastProgress, vc_globalVideoElement } from "@/app/(main)/_features/video-core/video-core-atoms"
import { useHandleStartDebridStream } from "@/app/(main)/entry/_containers/debrid-stream/_lib/handle-debrid-stream"
import { useHandleStartTorrentStream } from "@/app/(main)/entry/_containers/torrent-stream/_lib/handle-torrent-stream"
import { useWebsocketMessageListener, useWebsocketSender } from "@/app/(main)/_hooks/handle-websockets"
import { clientIdAtom } from "@/app/websocket-provider"
import { logger } from "@/lib/helpers/debug"
import { WSEvents } from "@/lib/server/ws-events"
import { getHalfRttSeconds } from "@/lib/server/ws-latency"
import { useNakamaJoinWatchRoomStream } from "@/api/hooks/nakama.hooks"
import { useAtomValue, useSetAtom } from "jotai"
import React from "react"
import { currentWatchRoomAtom, optedOutStreamRoomIdAtom } from "./nakama-manager"

// Same-instance watch-room player sync.
//
// Each member plays in their own browser; the server relays control ACTIONS between
// members. This hook (mounted once, in NakamaManager) bridges the local videocore player
// to that relay:
//   - emit NAKAMA_ROOM_PLAYBACK_STATUS on local play/pause/seek (only when allowed to
//     control), and
//   - apply NAKAMA_ROOM_PLAYBACK_SYNC from the server to the local player.
//
// Position + play/pause only — never tracks, so everyone keeps their own audio/subtitle
// (unless the host turns on "force my tracks", handled separately below).
//
// Echo guard: applying a remote action makes the player fire play/pause/seeked, which
// would re-broadcast and loop. We suppress emits for a short window after applying.

type RoomPlaybackSync = Nakama_RoomPlaybackStatusPayload

// Map the local player's source type to the room's stream type. "localfile" → "file"; the
// others share names. "url"/"nakama"/undefined have no shared source → empty (followers skip).
function nakamaStreamType(t: NativePlayer_StreamType | undefined): Nakama_WatchPartyStreamType {
    switch (t) {
        case "localfile": return "file"
        case "torrent": return "torrent"
        case "debrid": return "debrid"
        default: return "" as Nakama_WatchPartyStreamType
    }
}

const ECHO_GUARD_MS = 2000 // stop-echo guard (covers the ~700ms terminate)
const SEEK_THRESHOLD = 0.75 // only seek when off by more than this (avoids jitter)
// State-matched echo suppression for play/pause/seek: after applying a remote state, the
// player fires play/pause/seeked events that would re-broadcast and loop. We suppress an
// emit ONLY when the player still matches what we just applied (a real echo) within this
// window. A genuine local action diverges from the applied state and emits immediately —
// so it's robust to a late event (buffering) without swallowing real input like a timer would.
const APPLY_ECHO_WINDOW_MS = 2500
const APPLY_ECHO_SEEK_TOL = 1.5
const HEARTBEAT_MS = 1000 // how often the controller reports its position (re-anchors followers after a seek/buffer)
// Smooth-convergence tuning (followers only). Instead of hard-seeking on every bit of drift
// (which stutters), a follower nudges its playbackRate a few percent to GLIDE back into sync:
//   |drift| < DEADBAND        -> normal speed (already in sync)
//   DEADBAND..HARD_SEEK_DRIFT -> rate = 1 + clamp(drift*GAIN, ±MAX); eases in, never jumps
//   > HARD_SEEK_DRIFT         -> hard seek (a real gap from a seek/buffer; snap instantly)
// NUDGE_MAX 0.05 = ±5% ≈ a barely-perceptible pitch shift (a semitone is ~6%); GAIN makes the
// nudge proportional so it shrinks as it converges (no oscillation). Steady-state drift stays
// small because the server fans out fresh positions every 500ms, so the nudge is usually <2%.
const SYNC_DEADBAND = 0.08
const HARD_SEEK_DRIFT = 0.6
const NUDGE_GAIN = 0.12
const NUDGE_MAX = 0.05

export function useWatchRoomPlayerSync() {
    const room = useAtomValue(currentWatchRoomAtom)
    const clientId = useAtomValue(clientIdAtom)
    const videoElement = useAtomValue(vc_globalVideoElement)
    const lastProgress = useAtomValue(vc_globalLastProgress)
    const audioManager = useAtomValue(vc_audioManager)
    const subtitleManager = useAtomValue(vc_subtitleManager)
    const mediaCaptionsManager = useAtomValue(vc_mediaCaptionsManager)
    const nativeState = useAtomValue(nativePlayer_stateAtom)
    const playbackInfo = nativeState.playbackInfo
    const playerActive = nativeState.active
    const requestTerminate = useSetAtom(nativePlayer_terminateRequestedAtom)
    const { sendMessage } = useWebsocketSender()

    // Am I allowed to drive playback in this room? (host always; others if granted)
    const canControl = React.useMemo(() => {
        if (!room?.participants) return false
        const me = Object.values(room.participants).find(p => p.clientId === clientId)
        return !!me && (!!me.isHost || !!me.canControl)
    }, [room, clientId])

    const amHost = React.useMemo(() => {
        if (!room?.participants) return false
        const me = Object.values(room.participants).find(p => p.clientId === clientId)
        return !!me?.isHost
    }, [room, clientId])

    // Am I the effective controller (the one driving)? The controller must NEVER auto-follow
    // its own action — that re-launches the stream it just started (the room's lastPlayback
    // reflects the controller's own start), hammering the CDN with restarts. Only followers
    // auto-start.
    const amController = React.useMemo(() => {
        if (!room?.participants || !room.controllerKey) return false
        const myEntry = Object.entries(room.participants).find(([, p]) => p.clientId === clientId)
        return !!myEntry && myEntry[0] === room.controllerKey
    }, [room, clientId])

    const forceHostTracks = !!room?.forceHostTracks

    // Suppress emits while we're applying a remote action (prevents feedback loops).
    const applyingRemoteUntil = React.useRef(0)
    // The last play/pause/seek state we applied from the controller — used to recognize and
    // drop the echo events the apply itself fires (state-matched, not a blind time window).
    const lastAppliedRef = React.useRef<{ paused: boolean, currentTime: number, at: number } | null>(null)

    // DIAGNOSTIC (temporary): report the hook's view of the player whenever it changes, so we can
    // see if/when this hook actually observes vc_videoElement become non-null during room playback
    // (the server log shows when videocore mounts; this shows whether THIS hook sees it).
    React.useEffect(() => {
        if (!room) return
        sendMessage({
            type: WSEvents.NAKAMA_ROOM_DEBUG,
            payload: `hook-state video=${!!videoElement} active=${playerActive} playingMedia=${playbackInfo?.media?.id ?? 0} `
                + `canCtrl=${canControl} amCtrl=${amController}`,
        })
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [videoElement, playerActive, room?.id, canControl, amController])

    // Only a FOLLOWER nudges its playbackRate to converge; the driver always plays at normal
    // speed. The driver returns early in apply (never reaching the nudge), so if this client just
    // BECAME the driver (control handoff) it could be left at a leftover nudged rate — reset it.
    React.useEffect(() => {
        if (videoElement && canControl && amController && videoElement.playbackRate !== 1) {
            videoElement.playbackRate = 1
        }
    }, [videoElement, canControl, amController])

    // ---- Follow the controller into the episode (auto-start) ----
    // The sync above only adjusts an EXISTING player. When the controller starts an episode
    // a follower isn't watching yet, the follower has no player to adjust — so we kick off
    // the same stream here, then the position/play-pause sync takes over once it loads.
    const debridStart = useHandleStartDebridStream()
    const torrentStart = useHandleStartTorrentStream()
    const joinRoomStream = useNakamaJoinWatchRoomStream()
    const optedOutRoomId = useAtomValue(optedOutStreamRoomIdAtom)
    // The media+episode we last kicked off, so a burst of syncs doesn't restart it.
    // ponytail: a failed start stays latched on its key until the controller picks a
    // different episode (the next distinct key clears it). Acceptable: retrying the same
    // failed auto-select immediately would just fail again.
    const autoStartingKeyRef = React.useRef("")

    // startRoomStream launches the room's stream for this client. Debrid reuses the host's
    // already-resolved link (join-stream endpoint, no re-selection); torrent auto-selects.
    const startRoomStream = React.useCallback((p: RoomPlaybackSync) => {
        const key = `${p.mediaId}:${p.episodeNumber}:${p.streamType}`
        if (autoStartingKeyRef.current === key) return
        if (debridStart.isPending || torrentStart.isPending || joinRoomStream.isPending) return
        autoStartingKeyRef.current = key
        if (p.streamType === "debrid" && room?.id) {
            logger("NAKAMA ROOM SYNC").info("Joining room debrid stream (shared link)", p.mediaId, p.episodeNumber)
            joinRoomStream.mutate({ roomId: room.id, clientId: clientId || "", playbackType: debridStart.getResolvedPlaybackType() })
        } else if (p.streamType === "torrent") {
            torrentStart.handleAutoSelectStream({ mediaId: p.mediaId, episodeNumber: p.episodeNumber, aniDBEpisode: p.aniDbEpisode || "" })
        }
    }, [room, clientId, debridStart, torrentStart, joinRoomStream])

    const maybeAutoStart = React.useCallback((p: RoomPlaybackSync) => {
        if (!p || p.stopped || !p.mediaId || !p.episodeNumber) return
        // Only the ACTIVE DRIVER (can control AND is the controller) skips following — it
        // started the stream itself. A member promoted to controllerKey but unable to control
        // still has no stream, so it must follow.
        if (canControl && amController) return
        // Opted out of this room's stream (closed it, or joined while it was already live)?
        // Don't auto-open — the "Join room stream" button does.
        if (optedOutRoomId === p.roomId) return
        // Already playing/loading this exact media+episode? Let the position sync handle it.
        const playingThis = (
            (playbackInfo?.media?.id === p.mediaId && playbackInfo?.episode?.episodeNumber === p.episodeNumber)
            || (!!videoElement && lastProgress?.mediaId === p.mediaId && lastProgress?.progressNumber === p.episodeNumber)
        )
        if (playingThis) {
            autoStartingKeyRef.current = ""
            return
        }
        if (p.streamType !== "debrid" && p.streamType !== "torrent") {
            logger("NAKAMA ROOM SYNC").warning("Cannot auto-follow stream type", p.streamType)
            return
        }
        startRoomStream(p)
    }, [canControl, amController, optedOutRoomId, videoElement, lastProgress, playbackInfo, startRoomStream])

    // NOTE: no late-join auto-open. Joining a room that already has a live stream surfaces the
    // "Join room stream" button (button-only) instead of force-opening. Auto-open happens only
    // when the controller STARTS while you're present (a live sync arriving for a non-opted-out
    // member), handled in the apply listener below.

    // ---- Emit stop when the controller ends the episode ----
    // The player going active=false (closed) while we drive the room = "stop for everyone",
    // the mirror of auto-start. Episode SWITCHES keep the player active (just swap media), so
    // they don't trip this. Skip while applying a remote stop (echo guard) so it doesn't loop.
    const setOptedOut = useSetAtom(optedOutStreamRoomIdAtom)
    // Did the player actually come alive (a <video> mounted) this open? A server ABORT of a
    // failed open also flips active true->false but never mounts a video — that is NOT a user
    // close, so it must not opt us out. Opting out there would wedge auto-follow AND make every
    // "Join room stream" retry re-fail (the original "follower never plays the whole session").
    const everHadVideoRef = React.useRef(false)
    React.useEffect(() => {
        if (videoElement) everHadVideoRef.current = true
    }, [videoElement])
    const prevActiveRef = React.useRef(playerActive)
    React.useEffect(() => {
        const was = prevActiveRef.current
        prevActiveRef.current = playerActive
        if (!was || playerActive) return // only on true -> false
        const hadVideo = everHadVideoRef.current
        everHadVideoRef.current = false // reset for the next open
        if (!room) return
        if (Date.now() < applyingRemoteUntil.current) return // teardown caused by a remote stop, not a user close
        // Open failed/aborted before showing any video => not a user close. Don't opt out (so
        // auto-follow can recover and the Join button still works), don't emit a stop.
        if (!hadVideo) return
        if (canControl && amController) {
            // The driver closed the episode => stop for everyone (mirror of auto-start).
            sendMessage({
                type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS,
                payload: {
                    roomId: room.id, stopped: true, paused: true, currentTime: 0, duration: 0,
                    mediaId: 0, episodeNumber: 0, aniDbEpisode: "", streamType: nakamaStreamType(undefined),
                } satisfies RoomPlaybackSync,
            })
        } else if (room.playbackActive) {
            // A follower closed the player => opt out so the room's heartbeat doesn't re-open us.
            // Leaving stays left; the "Join room stream" button brings it back.
            setOptedOut(room.id)
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [playerActive])

    // ---- Emit local control actions ----
    React.useEffect(() => {
        if (!videoElement || !room) return
        const player = videoElement

        function buildPayload(heartbeat: boolean): RoomPlaybackSync {
            // Buffering hold: a driver whose player has STALLED (mid-rebuffer / seeking, not user-
            // paused) must not keep anchoring the room to its frozen position with paused=false —
            // that drags every follower backward once per heartbeat (the "constant pull-back": the
            // driver sticks at t=250.9 while a follower plays to 251.5 and gets seek->250.9 every
            // second). Report the stall as a transient pause so followers HOLD instead of rewind;
            // when the buffer fills, paused flips back false and everyone resumes together. Only on
            // the heartbeat — discrete play/pause/seek emits must carry the player's true state.
            const stalled = heartbeat && !player.paused && (player.seeking || player.readyState < 3)
            const effectivePaused = player.paused || stalled
            return {
                roomId: room!.id,
                paused: effectivePaused,
                // Lead by our own uplink latency while playing, so by the time this reaches the
                // server and is fanned to followers (who add their own downlink) everyone lands on
                // our true current frame. No lead while paused/stalled — position isn't advancing.
                currentTime: player.currentTime + (effectivePaused ? 0 : getHalfRttSeconds()),
                duration: isFinite(player.duration) ? player.duration : 0,
                // Prefer the global nativePlayer playbackInfo: lastProgress comes from
                // vc_lastKnownProgress which (like vc_videoElement) is bridged from the scoped
                // store and can lag/stay null, making emits go out with mediaId 0 — a follower's
                // maybeAutoStart drops those (no media to open), so Tenji never opened when Denshi
                // hosted. playbackInfo.media.id is the reliable global identity.
                mediaId: playbackInfo?.media?.id ?? lastProgress?.mediaId ?? 0,
                episodeNumber: playbackInfo?.episode?.episodeNumber ?? lastProgress?.progressNumber ?? 0,
                // Source identity so a follower can start the SAME stream (debrid/torrent).
                aniDbEpisode: playbackInfo?.episode?.aniDBEpisode ?? "",
                streamType: nakamaStreamType(playbackInfo?.streamType),
                heartbeat: heartbeat || undefined,
            }
        }

        function emit(isSeek: boolean) {
            if (!canControl) return
            // Buffering guard: a player stalling at a seek target fires play/pause as it rebuffers —
            // don't broadcast those (only genuine toggles; a seek always passes). The heartbeat
            // carries the settled paused state once buffering clears.
            if (!isSeek && (player.seeking || player.readyState < 3)) return
            // Drop the echo of a state we were just told to be in. A genuine local action
            // (different paused state, or a seek away from the applied position) diverges and
            // passes through immediately.
            const la = lastAppliedRef.current
            if (la && (Date.now() - la.at) < APPLY_ECHO_WINDOW_MS
                && la.paused === player.paused
                && Math.abs(player.currentTime - la.currentTime) < APPLY_ECHO_SEEK_TOL) {
                return
            }

            const payload = buildPayload(false)

            // When the host forces tracks, the host (and only the host) carries their
            // current audio/subtitle selection so members can mirror it.
            if (forceHostTracks && amHost) {
                payload.audioTrack = audioManager?.getSelectedTrackNumberOrNull?.() ?? undefined
                payload.subtitleTrack = subtitleManager?.getSelectedTrackNumberOrNull?.()
                    ?? mediaCaptionsManager?.getSelectedTrackIndexOrNull?.()
                    ?? undefined
            }

            sendMessage({ type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS, payload })
        }

        const onPlay = () => emit(false)
        const onPause = () => emit(false)
        const onSeeked = () => emit(true)
        player.addEventListener("play", onPlay)
        player.addEventListener("pause", onPause)
        player.addEventListener("seeked", onSeeked)

        // Heartbeat: the active driver broadcasts its position every couple seconds so
        // followers reconcile drift and late/desynced players catch up — discrete play/pause/
        // seek events alone never correct steady-playback drift. Tracks omitted (event-only).
        let hb: ReturnType<typeof setInterval> | undefined
        if (canControl && amController) {
            hb = setInterval(() => {
                if (!room) return
                sendMessage({ type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS, payload: buildPayload(true) })
            }, HEARTBEAT_MS)
        }

        return () => {
            if (hb) clearInterval(hb)
            player.removeEventListener("play", onPlay)
            player.removeEventListener("pause", onPause)
            player.removeEventListener("seeked", onSeeked)
        }
    }, [videoElement, room, canControl, amController, amHost, forceHostTracks, lastProgress, audioManager, subtitleManager,
        mediaCaptionsManager, playbackInfo, sendMessage])

    // ---- Apply incoming sync ----
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_PLAYBACK_SYNC,
        deps: [videoElement, canControl, amController, forceHostTracks, audioManager, subtitleManager, mediaCaptionsManager, maybeAutoStart, requestTerminate],
        onMessage: (p: RoomPlaybackSync | null) => {
            if (!p) return

            // DIAGNOSTIC (temporary): log the GATE state on every discrete sync, BEFORE the
            // videoElement early-return — the regular "apply recv" line only fires AFTER that gate,
            // so a follower whose videoElement is null is otherwise invisible. video:false here while
            // the server shows videocore running = the hook isn't seeing the player's video element.
            if (!p.heartbeat) {
                sendMessage({
                    type: WSEvents.NAKAMA_ROOM_DEBUG,
                    payload: `recv-gate{video:${!!videoElement},active:${playerActive},canCtrl:${canControl},amCtrl:${amController},`
                        + `playingMedia:${playbackInfo?.media?.id ?? 0}} p{paused:${p.paused},t:${(p.currentTime ?? 0).toFixed(1)},stop:${!!p.stopped}}`,
                })
            }

            // Controller ended the episode: stop ours too (mirror of auto-start). Guard the
            // emit side for the duration of our terminate (>700ms) so it doesn't echo back.
            if (p.stopped) {
                applyingRemoteUntil.current = Date.now() + 2000
                requestTerminate(c => c + 1)
                return
            }

            // Follow the controller into the episode if we're not already playing it. When we
            // have no player yet (or a different episode) this kicks off the stream and returns;
            // the position sync below applies once the player is up.
            maybeAutoStart(p)
            if (!videoElement) return

            // NO local-controller guard here. The server already excludes the sender from every
            // discrete relay AND excludes the live driver from the position ticker, so a sync only
            // ever arrives from ANOTHER member who is the current driver — never our own echo.
            // Gating on the local amController atom was the Issue-A bug: control handoff broadcasts
            // room-state only ONCE (on the transition), so a missed/raced push left this client's
            // amController stale-true after a peer took control, and it then ignored every sync from
            // the new driver ("Tenji to Denshi did nothing"). Apply unconditionally; the source is
            // guaranteed not to be us.

            // Record the state we're applying so the play/pause/seeked events it fires are
            // recognized as echoes and not re-broadcast (state-matched, robust to late events).
            lastAppliedRef.current = { paused: p.paused, currentTime: p.currentTime, at: Date.now() }

            // Discrete actions snap precisely; heartbeats converge SMOOTHLY via playbackRate
            // (see the tuning constants) so steady playback doesn't stutter from constant re-seeks.
            let action = "none"
            // Lead the target by our own downlink latency while playing, so we land on the
            // controller's TRUE current frame (it already led by its uplink). No lead while paused.
            const target = isFinite(p.currentTime) ? p.currentTime + (p.paused ? 0 : getHalfRttSeconds()) : videoElement.currentTime
            const drift = target - videoElement.currentTime
            const ad = Math.abs(drift)
            if (!p.heartbeat) {
                if (ad > SEEK_THRESHOLD) {
                    action = `seek->${target.toFixed(1)}`
                    videoElement.currentTime = target
                }
                videoElement.playbackRate = 1 // a real action -> normal speed
            } else if (ad > HARD_SEEK_DRIFT) {
                action = `seek->${target.toFixed(1)}`
                videoElement.currentTime = target
                videoElement.playbackRate = 1
            } else if (ad > SYNC_DEADBAND) {
                const off = Math.max(-NUDGE_MAX, Math.min(NUDGE_MAX, drift * NUDGE_GAIN))
                videoElement.playbackRate = 1 + off // glide toward the controller (no log: continuous)
            } else if (videoElement.playbackRate !== 1) {
                videoElement.playbackRate = 1 // converged -> normal speed
            }
            if (p.paused && !videoElement.paused) {
                action += " pause"
                videoElement.pause()
            } else if (!p.paused && videoElement.paused) {
                action += " play"
                videoElement.play().catch(() => { })
            }
            // DIAGNOSTIC (temporary): only log when something actually applied (or a discrete
            // sync) — heartbeats with action=[none] are once-per-second and drowned the log.
            if (!p.heartbeat || action !== "none") {
                sendMessage({
                    type: WSEvents.NAKAMA_ROOM_DEBUG,
                    payload: `apply recv{paused:${p.paused},t:${p.currentTime.toFixed(1)},hb:${!!p.heartbeat}} `
                        + `local{paused:${videoElement.paused},t:${videoElement.currentTime.toFixed(1)}} action=[${action.trim()}]`,
                })
            }

            // Force-host-tracks: mirror the host's audio/subtitle selection.
            if (forceHostTracks) {
                if (typeof p.audioTrack === "number" && audioManager) {
                    audioManager.selectTrack(p.audioTrack)
                }
                if (typeof p.subtitleTrack === "number") {
                    if (subtitleManager) {
                        p.subtitleTrack === -1 ? subtitleManager.setNoTrack() : subtitleManager.selectTrack(p.subtitleTrack)
                    } else if (mediaCaptionsManager) {
                        mediaCaptionsManager.selectTrack(p.subtitleTrack)
                    }
                }
            }
        },
    })
}

// useRoomStreamJoin powers the "Join room stream" button. canJoin is true when the room has a
// live stream this client isn't already watching (and isn't driving). join() clears the opt-out
// and starts — debrid reuses the host's shared link; torrent auto-selects.
export function useRoomStreamJoin() {
    const room = useAtomValue(currentWatchRoomAtom)
    const clientId = useAtomValue(clientIdAtom)
    const setOptedOut = useSetAtom(optedOutStreamRoomIdAtom)
    const joinRoomStream = useNakamaJoinWatchRoomStream()
    const debridStart = useHandleStartDebridStream()
    const torrentStart = useHandleStartTorrentStream()
    const playbackInfo = useAtomValue(nativePlayer_stateAtom).playbackInfo

    const mi = room?.currentMediaInfo
    const amController = React.useMemo(() => {
        if (!room?.participants || !room.controllerKey) return false
        const e = Object.entries(room.participants).find(([, pp]) => pp.clientId === clientId)
        return !!e && e[0] === room.controllerKey && (!!e[1].isHost || !!e[1].canControl)
    }, [room, clientId])

    const watchingThis = !!playbackInfo && playbackInfo.media?.id === mi?.mediaId
        && playbackInfo.episode?.episodeNumber === mi?.episodeNumber

    const canJoin = !!room?.playbackActive && !!mi && !watchingThis && !amController

    const join = React.useCallback(() => {
        if (!room?.id || !mi) return
        setOptedOut(null)
        if (mi.streamType === "torrent") {
            torrentStart.handleAutoSelectStream({ mediaId: mi.mediaId, episodeNumber: mi.episodeNumber, aniDBEpisode: mi.aniDbEpisode || "" })
        } else {
            joinRoomStream.mutate({ roomId: room.id, clientId: clientId || "", playbackType: debridStart.getResolvedPlaybackType() })
        }
    }, [room, mi, clientId, setOptedOut, joinRoomStream, debridStart, torrentStart])

    return { canJoin, join, isPending: joinRoomStream.isPending || debridStart.isPending || torrentStart.isPending }
}
