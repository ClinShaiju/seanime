import { Nakama_RoomPlaybackStatusPayload, Nakama_WatchPartyStreamType, NativePlayer_StreamType } from "@/api/generated/types"
import { nativePlayer_stateAtom, nativePlayer_terminateRequestedAtom } from "@/app/(main)/_features/native-player/native-player.atoms"
import { vc_audioManager, vc_mediaCaptionsManager, vc_subtitleManager } from "@/app/(main)/_features/video-core/video-core"
import { vc_lastKnownProgress, vc_videoElement } from "@/app/(main)/_features/video-core/video-core-atoms"
import { useHandleStartDebridStream } from "@/app/(main)/entry/_containers/debrid-stream/_lib/handle-debrid-stream"
import { useHandleStartTorrentStream } from "@/app/(main)/entry/_containers/torrent-stream/_lib/handle-torrent-stream"
import { useWebsocketMessageListener, useWebsocketSender } from "@/app/(main)/_hooks/handle-websockets"
import { clientIdAtom } from "@/app/websocket-provider"
import { logger } from "@/lib/helpers/debug"
import { WSEvents } from "@/lib/server/ws-events"
import { useAtomValue, useSetAtom } from "jotai"
import React from "react"
import { currentWatchRoomAtom } from "./nakama-manager"

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
const HEARTBEAT_MS = 2000 // how often the active driver re-broadcasts its position
const HEARTBEAT_DRIFT = 2.0 // a follower only re-seeks on a heartbeat when off by more than this (avoids constant micro-jumps)

export function useWatchRoomPlayerSync() {
    const room = useAtomValue(currentWatchRoomAtom)
    const clientId = useAtomValue(clientIdAtom)
    const videoElement = useAtomValue(vc_videoElement)
    const lastProgress = useAtomValue(vc_lastKnownProgress)
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

    // ---- Follow the controller into the episode (auto-start) ----
    // The sync above only adjusts an EXISTING player. When the controller starts an episode
    // a follower isn't watching yet, the follower has no player to adjust — so we kick off
    // the same stream here, then the position/play-pause sync takes over once it loads.
    const debridStart = useHandleStartDebridStream()
    const torrentStart = useHandleStartTorrentStream()
    // The media+episode we last kicked off, so a burst of syncs doesn't restart it.
    // ponytail: a failed start stays latched on its key until the controller picks a
    // different episode (the next distinct key clears it). Acceptable: retrying the same
    // failed auto-select immediately would just fail again.
    const autoStartingKeyRef = React.useRef("")

    const maybeAutoStart = React.useCallback((p: RoomPlaybackSync) => {
        if (!p || p.stopped || !p.mediaId || !p.episodeNumber) return
        // Only the ACTIVE DRIVER (can control AND is the controller) skips following — it
        // started the stream itself. A member who merely got promoted to controllerKey but
        // can't actually control (e.g. after a host blip) still has no stream, so it must
        // follow; guarding on amController alone would wrongly freeze it out.
        if (canControl && amController) return
        // Already playing/loading this exact media+episode? Let the position sync handle it.
        // Check the active player's TARGET (playbackInfo) — true even while the stream is still
        // loading/stalled, unlike lastProgress which needs playback to have progressed.
        const playingThis = (
            (playbackInfo?.media?.id === p.mediaId && playbackInfo?.episode?.episodeNumber === p.episodeNumber)
            || (!!videoElement && lastProgress?.mediaId === p.mediaId && lastProgress?.progressNumber === p.episodeNumber)
        )
        if (playingThis) {
            autoStartingKeyRef.current = ""
            return
        }
        // Cross-instance rooms can't share local files / online streams — only debrid &
        // torrent resolve the same source on another machine.
        if (p.streamType !== "debrid" && p.streamType !== "torrent") {
            logger("NAKAMA ROOM SYNC").warning("Cannot auto-follow stream type", p.streamType)
            return
        }
        const key = `${p.mediaId}:${p.episodeNumber}:${p.streamType}`
        if (autoStartingKeyRef.current === key) return
        if (debridStart.isPending || torrentStart.isPending) return
        autoStartingKeyRef.current = key
        const args = { mediaId: p.mediaId, episodeNumber: p.episodeNumber, aniDBEpisode: p.aniDbEpisode || "" }
        logger("NAKAMA ROOM SYNC").info("Auto-starting room stream", args, p.streamType)
        if (p.streamType === "torrent") {
            torrentStart.handleAutoSelectStream(args)
        } else {
            debridStart.handleAutoSelectStream(args)
        }
    }, [canControl, amController, videoElement, lastProgress, playbackInfo, debridStart, torrentStart])

    // Late join / room state refresh: if the room already has a playback action, follow it.
    React.useEffect(() => {
        if (room?.lastPlayback) maybeAutoStart(room.lastPlayback)
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [room?.id, room?.lastPlayback?.mediaId, room?.lastPlayback?.episodeNumber, room?.lastPlayback?.streamType])

    // ---- Emit stop when the controller ends the episode ----
    // The player going active=false (closed) while we drive the room = "stop for everyone",
    // the mirror of auto-start. Episode SWITCHES keep the player active (just swap media), so
    // they don't trip this. Skip while applying a remote stop (echo guard) so it doesn't loop.
    const prevActiveRef = React.useRef(playerActive)
    React.useEffect(() => {
        const was = prevActiveRef.current
        prevActiveRef.current = playerActive
        if (!was || playerActive) return // only on true -> false
        if (!room || !canControl) return
        if (Date.now() < applyingRemoteUntil.current) return
        sendMessage({
            type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS,
            payload: {
                roomId: room.id, stopped: true, paused: true, currentTime: 0, duration: 0,
                mediaId: 0, episodeNumber: 0, aniDbEpisode: "", streamType: nakamaStreamType(undefined),
            } satisfies RoomPlaybackSync,
        })
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [playerActive])

    // ---- Emit local control actions ----
    React.useEffect(() => {
        if (!videoElement || !room) return
        const player = videoElement

        function buildPayload(heartbeat: boolean): RoomPlaybackSync {
            return {
                roomId: room!.id,
                paused: player.paused,
                currentTime: player.currentTime,
                duration: isFinite(player.duration) ? player.duration : 0,
                mediaId: lastProgress?.mediaId ?? 0,
                episodeNumber: lastProgress?.progressNumber ?? 0,
                // Source identity so a follower can start the SAME stream (debrid/torrent).
                aniDbEpisode: playbackInfo?.episode?.aniDBEpisode ?? "",
                streamType: nakamaStreamType(playbackInfo?.streamType),
                heartbeat: heartbeat || undefined,
            }
        }

        function emit() {
            if (!canControl) return
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

        player.addEventListener("play", emit)
        player.addEventListener("pause", emit)
        player.addEventListener("seeked", emit)

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
            player.removeEventListener("play", emit)
            player.removeEventListener("pause", emit)
            player.removeEventListener("seeked", emit)
        }
    }, [videoElement, room, canControl, amController, amHost, forceHostTracks, lastProgress, audioManager, subtitleManager,
        mediaCaptionsManager, playbackInfo, sendMessage])

    // ---- Apply incoming sync ----
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_PLAYBACK_SYNC,
        deps: [videoElement, forceHostTracks, audioManager, subtitleManager, mediaCaptionsManager, maybeAutoStart, requestTerminate],
        onMessage: (p: RoomPlaybackSync | null) => {
            if (!p) return

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

            // Record the state we're applying so the play/pause/seeked events it fires are
            // recognized as echoes and not re-broadcast (state-matched, robust to late events).
            lastAppliedRef.current = { paused: p.paused, currentTime: p.currentTime, at: Date.now() }

            // Heartbeats only correct large drift (steady playback naturally wanders a little);
            // discrete seeks apply precisely.
            const seekThreshold = p.heartbeat ? HEARTBEAT_DRIFT : SEEK_THRESHOLD
            if (isFinite(p.currentTime) && Math.abs(videoElement.currentTime - p.currentTime) > seekThreshold) {
                videoElement.currentTime = p.currentTime
            }
            if (p.paused && !videoElement.paused) {
                videoElement.pause()
            } else if (!p.paused && videoElement.paused) {
                videoElement.play().catch(() => { })
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
