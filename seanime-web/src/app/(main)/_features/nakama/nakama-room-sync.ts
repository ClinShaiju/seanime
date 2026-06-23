import { vc_audioManager, vc_mediaCaptionsManager, vc_subtitleManager } from "@/app/(main)/_features/video-core/video-core"
import { vc_lastKnownProgress, vc_videoElement } from "@/app/(main)/_features/video-core/video-core-atoms"
import { useWebsocketMessageListener, useWebsocketSender } from "@/app/(main)/_hooks/handle-websockets"
import { clientIdAtom } from "@/app/websocket-provider"
import { WSEvents } from "@/lib/server/ws-events"
import { useAtomValue } from "jotai"
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

type RoomPlaybackSync = {
    roomId: string
    paused: boolean
    currentTime: number
    duration: number
    mediaId: number
    episodeNumber: number
    audioTrack?: number | null
    subtitleTrack?: number | null
}

const ECHO_GUARD_MS = 800
const SEEK_THRESHOLD = 0.75 // only seek when off by more than this (avoids jitter)

export function useWatchRoomPlayerSync() {
    const room = useAtomValue(currentWatchRoomAtom)
    const clientId = useAtomValue(clientIdAtom)
    const videoElement = useAtomValue(vc_videoElement)
    const lastProgress = useAtomValue(vc_lastKnownProgress)
    const audioManager = useAtomValue(vc_audioManager)
    const subtitleManager = useAtomValue(vc_subtitleManager)
    const mediaCaptionsManager = useAtomValue(vc_mediaCaptionsManager)
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

    const forceHostTracks = !!room?.forceHostTracks

    // Suppress emits while we're applying a remote action (prevents feedback loops).
    const applyingRemoteUntil = React.useRef(0)

    // ---- Emit local control actions ----
    React.useEffect(() => {
        if (!videoElement || !room) return
        const player = videoElement

        function emit() {
            if (!canControl) return
            if (Date.now() < applyingRemoteUntil.current) return

            const payload: RoomPlaybackSync = {
                roomId: room!.id,
                paused: player.paused,
                currentTime: player.currentTime,
                duration: isFinite(player.duration) ? player.duration : 0,
                mediaId: lastProgress?.mediaId ?? 0,
                episodeNumber: lastProgress?.progressNumber ?? 0,
            }

            // When the host forces tracks, the host (and only the host) carries their
            // current audio/subtitle selection so members can mirror it.
            if (forceHostTracks && amHost) {
                payload.audioTrack = audioManager?.getSelectedTrackNumberOrNull?.() ?? null
                payload.subtitleTrack = subtitleManager?.getSelectedTrackNumberOrNull?.()
                    ?? mediaCaptionsManager?.getSelectedTrackIndexOrNull?.()
                    ?? null
            }

            sendMessage({ type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS, payload })
        }

        player.addEventListener("play", emit)
        player.addEventListener("pause", emit)
        player.addEventListener("seeked", emit)
        return () => {
            player.removeEventListener("play", emit)
            player.removeEventListener("pause", emit)
            player.removeEventListener("seeked", emit)
        }
    }, [videoElement, room, canControl, amHost, forceHostTracks, lastProgress, audioManager, subtitleManager, mediaCaptionsManager,
        sendMessage])

    // ---- Apply incoming sync ----
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_PLAYBACK_SYNC,
        deps: [videoElement, forceHostTracks, audioManager, subtitleManager, mediaCaptionsManager],
        onMessage: (p: RoomPlaybackSync | null) => {
            if (!videoElement || !p) return

            // Suppress the play/pause/seeked events our own changes are about to fire.
            applyingRemoteUntil.current = Date.now() + ECHO_GUARD_MS

            if (isFinite(p.currentTime) && Math.abs(videoElement.currentTime - p.currentTime) > SEEK_THRESHOLD) {
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
