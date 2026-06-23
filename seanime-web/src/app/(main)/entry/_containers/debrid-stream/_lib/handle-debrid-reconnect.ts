import { DebridStartStream_Variables } from "@/api/generated/endpoint.types"
import { useDebridStartStream } from "@/api/hooks/debrid.hooks"
import { nativePlayer_stateAtom } from "@/app/(main)/_features/native-player/native-player.atoms"
import { websocketConnectedAtom } from "@/app/websocket-provider"
import { logger } from "@/lib/helpers/debug"
import { atom, useAtomValue } from "jotai"
import React from "react"

// lastDebridStreamStartAtom holds the variables of the last ACTIVE (non-preload) debrid
// stream, so it can be re-issued if the server restarts mid-playback.
export const lastDebridStreamStartAtom = atom<DebridStartStream_Variables | null>(null)

const log = logger("DEBRID RECONNECT")

// useDebridReconnectResume re-establishes a debrid stream that died because the server
// restarted (deploy/crash) mid-playback. When the websocket reconnects after having dropped
// while the native player was active, it re-issues the last start request once. The server
// reuses the already-resolved selection (in-memory if the process survived; the deduped,
// already-added torrent on a cold start — no new createtorrent), and the player resumes at
// the saved position via continuity (kept fresh by the periodic progress save). Mount once
// in the native player.
export function useDebridReconnectResume() {
    const wsConnected = useAtomValue(websocketConnectedAtom)
    const nativeState = useAtomValue(nativePlayer_stateAtom)
    const lastStart = useAtomValue(lastDebridStreamStartAtom)
    const { mutate: startStream } = useDebridStartStream()

    // True once the websocket has dropped while a stream was active — armed for a re-issue
    // when it comes back. Prevents re-issuing on a normal reconnect (no drop during playback).
    const droppedWhileActiveRef = React.useRef(false)

    React.useEffect(() => {
        const streamActive = nativeState.active && !!nativeState.playbackInfo

        if (!streamActive) {
            // Player ended / no stream → never re-issue something the user closed.
            droppedWhileActiveRef.current = false
            return
        }

        if (!wsConnected) {
            // Server went away while a stream was playing — arm the resume.
            droppedWhileActiveRef.current = true
            return
        }

        // Server is back after a drop and the stream is still active → re-issue once.
        if (droppedWhileActiveRef.current && lastStart) {
            droppedWhileActiveRef.current = false
            log.info("Server reconnected mid-stream — re-issuing debrid stream to resume", lastStart.mediaId, lastStart.episodeNumber)
            startStream({ ...lastStart, preload: false })
        }
    }, [wsConnected, nativeState.active, nativeState.playbackInfo, lastStart])
}
