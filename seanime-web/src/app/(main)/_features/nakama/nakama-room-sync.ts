import { Nakama_RoomPlaybackStatusPayload, Nakama_WatchPartyStreamType, NativePlayer_StreamType } from "@/api/generated/types"
import { mpvCore_stateAtom } from "@/app/(main)/_features/mpv-core/mpv-core.atoms"
import { nativePlayer_stateAtom, nativePlayer_terminateRequestedAtom } from "@/app/(main)/_features/native-player/native-player.atoms"
import { vc_audioManager, vc_mediaCaptionsManager, vc_subtitleManager } from "@/app/(main)/_features/video-core/video-core"
// Use the GLOBAL mirrors, not vc_videoElement / vc_lastKnownProgress directly: those are scoped to
// VideoCoreProvider (jotai-scope) and this hook runs app-wide in NakamaManager, OUTSIDE that scope,
// so the scoped atoms always read null here. Both VideoCoreGlobalBridge and MpvCorePlayerContent
// populate the player-agnostic vc_globalPlayerSyncControl, so this hook works for either player.
import { vc_globalLastProgress, vc_globalPlayerSyncControl } from "@/app/(main)/_features/video-core/video-core-atoms"
import { useHandleStartDebridStream } from "@/app/(main)/entry/_containers/debrid-stream/_lib/handle-debrid-stream"
import { useHandleStartTorrentStream } from "@/app/(main)/entry/_containers/torrent-stream/_lib/handle-torrent-stream"
import { useWebsocketMessageListener, useWebsocketSender } from "@/app/(main)/_hooks/handle-websockets"
import { clientIdAtom } from "@/app/websocket-provider"
import { logger } from "@/lib/helpers/debug"
import { WSEvents } from "@/lib/server/ws-events"
import { getHalfRttSeconds } from "@/lib/server/ws-latency"
import { useNakamaJoinWatchRoomStream } from "@/api/hooks/nakama.hooks"
import { __isElectronDesktop__ } from "@/types/constants"
import { useAtomValue, useSetAtom } from "jotai"
import React from "react"
import { currentWatchRoomAtom, optedOutStreamRoomIdAtom } from "./nakama-manager"
import { decideFollowerSync, roomStreamKey } from "./nakama-sync-reconcile"

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
// State-matched echo suppression for play/pause/seek: after applying a remote state, the
// player fires play/pause/seeked events that would re-broadcast and loop. We suppress an
// emit ONLY when the player still matches what we just applied (a real echo) within this
// window. A genuine local action diverges from the applied state and emits immediately —
// so it's robust to a late event (buffering) without swallowing real input like a timer would.
const APPLY_ECHO_WINDOW_MS = 2500
const APPLY_ECHO_SEEK_TOL = 1.5
const HEARTBEAT_MS = 1000 // how often the controller reports its position (re-anchors followers after a seek/buffer)
// After a heartbeat-driven hard seek, suppress the next one this long and let the nudge converge —
// a directstream follower (debrid/torrent http) resets its server connection on every seek, so
// back-to-back seeks thrash the stream. ponytail: fixed cooldown, not adaptive to drift magnitude.
const SEEK_COOLDOWN_MS = 2500

export function useWatchRoomPlayerSync() {
    const room = useAtomValue(currentWatchRoomAtom)
    const clientId = useAtomValue(clientIdAtom)
    // Player-agnostic sync control: populated by whichever player is active (VideoCore DOM
    // bridge or MpvCore native bridge). null when no player is mounted.
    const player = useAtomValue(vc_globalPlayerSyncControl)
    const lastProgress = useAtomValue(vc_globalLastProgress)
    const audioManager = useAtomValue(vc_audioManager)
    const subtitleManager = useAtomValue(vc_subtitleManager)
    const mediaCaptionsManager = useAtomValue(vc_mediaCaptionsManager)
    const nativeState = useAtomValue(nativePlayer_stateAtom)
    const mpvState = useAtomValue(mpvCore_stateAtom)
    // Either player being active counts — the sync control is populated by whichever one is.
    // Prefer nativeState.playbackInfo (carries streamType for the relay source identity); fall
    // back to mpvState for media/episode when MpvCore is the active player.
    const nativeInfo = nativeState.playbackInfo
    const mpvInfo = mpvState.playbackInfo
    const playbackInfo = nativeInfo || mpvInfo
    const playerActive = nativeState.active || mpvState.active
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
    // When this client follows via directstream (debrid/torrent http), the server serves ONE
    // byte-range at a time and a SEEK cancels the in-flight range -> the server's write fails with
    // "connection reset by peer" and the player rebuffers. A follower that hard-seeks on every
    // heartbeat drift thus thrashes its own stream: seek -> reset -> rebuffer -> fall behind -> seek.
    // So after a hard seek we hold off on the NEXT heartbeat-driven seek for a cooldown and let the
    // playbackRate nudge close the gap instead, giving the directstream time to re-establish. A
    // discrete (user-initiated) seek always applies — the user explicitly jumped.
    const lastHardSeekRef = React.useRef(0)
    // The driver's last reported position + wall-clock time, so a heartbeat can tell a LIVE
    // (advancing) driver from a FROZEN/stalled one (see decideFollowerSync / F25).
    const lastDriverReportRef = React.useRef<{ t: number, at: number } | null>(null)

    // DIAGNOSTIC (temporary): report the hook's view of the player whenever it changes, so we can
    // see if/when this hook actually observes vc_videoElement become non-null during room playback
    // (the server log shows when videocore mounts; this shows whether THIS hook sees it).
    React.useEffect(() => {
        if (!room) return
        sendMessage({
            type: WSEvents.NAKAMA_ROOM_DEBUG,
            payload: `hook-state video=${!!player} active=${playerActive} playingMedia=${playbackInfo?.media?.id ?? 0} `
                + `canCtrl=${canControl} amCtrl=${amController}`,
        })
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [player, playerActive, room?.id, canControl, amController])

    // Only a FOLLOWER nudges its playbackRate to converge; the driver always plays at normal
    // speed. The driver returns early in apply (never reaching the nudge), so if this client just
    // BECAME the driver (control handoff) it could be left at a leftover nudged rate — reset it.
    React.useEffect(() => {
        if (player && canControl && amController && player.playbackRate !== 1) {
            player.setPlaybackRate(1)
        }
    }, [player, canControl, amController])

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
            joinRoomStream.mutate({
                roomId: room.id,
                clientId: clientId || "",
                playbackType: debridStart.getResolvedPlaybackType(),
                directCdnCapable: __isElectronDesktop__,
            })
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
        // Opted out of THIS stream instance (closed it, or joined while it was already live)?
        // Keyed by room+media+episode (not bare roomId) so a NEW episode the controller starts
        // still auto-opens. The "Join room stream" button clears it.
        if (optedOutRoomId === roomStreamKey(p.roomId, p.mediaId, p.episodeNumber)) return
        // Already playing/loading this exact media+episode? Let the position sync handle it.
        const playingThis = (
            (playbackInfo?.media?.id === p.mediaId && playbackInfo?.episode?.episodeNumber === p.episodeNumber)
            || (!!player && lastProgress?.mediaId === p.mediaId && lastProgress?.progressNumber === p.episodeNumber)
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
    }, [canControl, amController, optedOutRoomId, player, lastProgress, playbackInfo, startRoomStream])

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
        if (player) everHadVideoRef.current = true
    }, [player])
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
        if (amHost) {
            // Only the HOST closing stops the episode for everyone (mirror of auto-start). A non-host
            // closing — even one currently driving — just opts itself out below; the rest of the room
            // keeps watching. (Previously the active driver stopped everyone, so a non-host controller
            // closing tore down the host's stream.)
            sendMessage({
                type: WSEvents.NAKAMA_ROOM_PLAYBACK_STATUS,
                payload: {
                    roomId: room.id, stopped: true, paused: true, currentTime: 0, duration: 0,
                    mediaId: 0, episodeNumber: 0, aniDbEpisode: "", streamType: nakamaStreamType(undefined),
                } satisfies RoomPlaybackSync,
            })
        } else if (room.playbackActive) {
            // A follower closed the player => opt out of THIS stream instance so the room's heartbeat
            // doesn't re-open it. A NEW episode (different key) still auto-opens; the "Join room
            // stream" button brings the current one back.
            setOptedOut(roomStreamKey(room.id, room.currentMediaInfo?.mediaId, room.currentMediaInfo?.episodeNumber))
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [playerActive])

    // ---- Emit local control actions ----
    React.useEffect(() => {
        if (!player || !room) return
        // TS can't narrow `player` into inner closures despite the guard above; alias once.
        const p = player

        function buildPayload(heartbeat: boolean): RoomPlaybackSync {
            // Buffering hold: a driver whose player has STALLED (mid-rebuffer / seeking, not user-
            // paused) must not keep anchoring the room to its frozen position with paused=false —
            // that drags every follower backward once per heartbeat (the "constant pull-back": the
            // driver sticks at t=250.9 while a follower plays to 251.5 and gets seek->250.9 every
            // second). Report the stall as a transient pause so followers HOLD instead of rewind;
            // when the buffer fills, paused flips back false and everyone resumes together. Only on
            // the heartbeat — discrete play/pause/seek emits must carry the player's true state.
            const stalled = heartbeat && !p.paused && (p.seeking || p.readyState < 3)
            const effectivePaused = p.paused || stalled
            return {
                roomId: room!.id,
                paused: effectivePaused,
                // Lead by our own uplink latency while playing, so by the time this reaches the
                // server and is fanned to followers (who add their own downlink) everyone lands on
                // our true current frame. No lead while paused/stalled — position isn't advancing.
                currentTime: p.currentTime + (effectivePaused ? 0 : getHalfRttSeconds()),
                duration: isFinite(p.duration) ? p.duration : 0,
                // Prefer the global nativePlayer playbackInfo: lastProgress comes from
                // vc_lastKnownProgress which (like vc_videoElement) is bridged from the scoped
                // store and can lag/stay null, making emits go out with mediaId 0 — a follower's
                // maybeAutoStart drops those (no media to open), so Tenji never opened when Denshi
                // hosted. playbackInfo.media.id is the reliable global identity.
                mediaId: playbackInfo?.media?.id ?? lastProgress?.mediaId ?? 0,
                episodeNumber: playbackInfo?.episode?.episodeNumber ?? lastProgress?.progressNumber ?? 0,
                // Source identity so a follower can start the SAME stream (debrid/torrent).
                aniDbEpisode: playbackInfo?.episode?.aniDBEpisode ?? "",
                streamType: nakamaStreamType(nativeInfo?.streamType ?? (mpvInfo?.playbackType as NativePlayer_StreamType | undefined)),
                heartbeat: heartbeat || undefined,
            }
        }

        function emit(isSeek: boolean) {
            if (!canControl) return
            // Teardown guard: when a player is closing, the DOM element (or MpvCore) resets to
            // currentTime=0/paused=true before unmounting, firing a last play/pause/seeked event.
            // Broadcasting that t=0 to followers seeks them to the start. Suppress emit when the
            // player reports t≈0 while we were well into playback — a genuine "start from 0" is
            // only valid when no prior position existed.
            if (p.currentTime < 0.5 && everHadVideoRef.current && lastAppliedRef.current && lastAppliedRef.current.currentTime > 5) return
            // Buffering guard: a player stalling at a seek target fires play/pause as it rebuffers —
            // don't broadcast those (only genuine toggles; a seek always passes). The heartbeat
            // carries the settled paused state once buffering clears.
            if (!isSeek && (p.seeking || p.readyState < 3)) return
            // Drop the echo of a state we were just told to be in. A genuine local action
            // (different paused state, or a seek away from the applied position) diverges and
            // passes through immediately.
            const la = lastAppliedRef.current
            if (la && (Date.now() - la.at) < APPLY_ECHO_WINDOW_MS
                && la.paused === p.paused
                && Math.abs(p.currentTime - la.currentTime) < APPLY_ECHO_SEEK_TOL) {
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
        // Discrete event listeners: VideoCore uses DOM addEventListener on the underlying video
        // element; MpvCore uses the subscribe() callback fired from its IPC event handlers. Both
        // produce the same immediate emit on play/pause/seek so followers track instantly.
        const domBridge = p.domElement ?? null
        let unsubNative: (() => void) | undefined
        if (domBridge) {
            domBridge.addEventListener("play", onPlay)
            domBridge.addEventListener("pause", onPause)
            domBridge.addEventListener("seeked", onSeeked)
        } else if (p.subscribe) {
            unsubNative = p.subscribe((action) => {
                if (action === "seeked") onSeeked()
                else if (action === "play") onPlay()
                else if (action === "pause") onPause()
            })
        }

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
            unsubNative?.()
            if (domBridge) {
                domBridge.removeEventListener("play", onPlay)
                domBridge.removeEventListener("pause", onPause)
                domBridge.removeEventListener("seeked", onSeeked)
            }
        }
    }, [player, room, canControl, amController, amHost, forceHostTracks, lastProgress, audioManager, subtitleManager,
        mediaCaptionsManager, playbackInfo, sendMessage])

    // ---- Apply incoming sync ----
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_PLAYBACK_SYNC,
        deps: [player, canControl, amController, forceHostTracks, audioManager, subtitleManager, mediaCaptionsManager, maybeAutoStart, requestTerminate],
        onMessage: (p: RoomPlaybackSync | null) => {
            if (!p) return

            // DIAGNOSTIC (temporary): log the GATE state on every discrete sync, BEFORE the
            // videoElement early-return — the regular "apply recv" line only fires AFTER that gate,
            // so a follower whose videoElement is null is otherwise invisible. video:false here while
            // the server shows videocore running = the hook isn't seeing the player's video element.
            if (!p.heartbeat) {
                sendMessage({
                    type: WSEvents.NAKAMA_ROOM_DEBUG,
                    payload: `recv-gate{video:${!!player},active:${playerActive},canCtrl:${canControl},amCtrl:${amController},`
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
            if (!player) return

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

            let action = "none"
            // Lead the target by our own downlink latency while playing, so we land on the
            // controller's TRUE current frame (it already led by its uplink). No lead while paused.
            const target = isFinite(p.currentTime) ? p.currentTime + (p.paused ? 0 : getHalfRttSeconds()) : player.currentTime
            const drift = target - player.currentTime

            // Is the driver's position feed LIVE (advancing ~wall-clock across heartbeats) or FROZEN
            // (stalled/backgrounded but still heartbeating paused:false — the iOS "stopped playback"
            // case)? We rewind to a live-but-behind driver (a real divergence, e.g. we auto-skipped
            // the OP and it didn't) but HOLD for a frozen one (rewinding to it every heartbeat is the
            // rubber-band). Computed only on heartbeats, from the driver's own reported position.
            let driverAdvancing = false
            if (p.heartbeat) {
                const prev = lastDriverReportRef.current
                const nowMs = Date.now()
                if (prev && !p.paused) {
                    const wall = (nowMs - prev.at) / 1000
                    driverAdvancing = wall > 0.2 && (p.currentTime - prev.t) >= wall * 0.5
                }
                lastDriverReportRef.current = { t: p.currentTime, at: nowMs }
            }

            // decideFollowerSync owns the reconciliation rule (unit-tested in
            // nakama-sync-reconcile.test.ts) so both convergence directions — including the rewind
            // the old code refused (F25) — are covered. Discrete actions snap; heartbeats nudge for
            // small drift and hard-seek for large.
            const decision = decideFollowerSync({
                isHeartbeat: !!p.heartbeat,
                drift,
                driverAdvancing,
                seekCooldownActive: (Date.now() - lastHardSeekRef.current) <= SEEK_COOLDOWN_MS,
            })
            if (decision.seek) {
                action = `seek->${target.toFixed(1)}`
                player.seek(target)
                lastHardSeekRef.current = Date.now() // a heartbeat right after must not re-seek
            }
            if (player.playbackRate !== decision.rate) player.setPlaybackRate(decision.rate)
            if (p.paused && !player.paused) {
                action += " pause"
                player.pause()
            } else if (!p.paused && player.paused) {
                action += " play"
                player.play()
            }
            // DIAGNOSTIC (temporary): only log when something actually applied (or a discrete
            // sync) — heartbeats with action=[none] are once-per-second and drowned the log.
            if (!p.heartbeat || action !== "none") {
                sendMessage({
                    type: WSEvents.NAKAMA_ROOM_DEBUG,
                    payload: `apply recv{paused:${p.paused},t:${p.currentTime.toFixed(1)},hb:${!!p.heartbeat}} `
                        + `local{paused:${player.paused},t:${player.currentTime.toFixed(1)}} action=[${action.trim()}]`,
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

    const watchingThis = !!playbackInfo && playbackInfo.media?.id === mi?.mediaId
        && playbackInfo.episode?.episodeNumber === mi?.episodeNumber

    // No controller exclusion: an ACTIVELY-driving controller is already watching the room's
    // media, so watchingThis hides the button for them anyway. Excluding amController wedged a
    // NON-HOST DRIVER who closed their player — they kept controllerKey (nothing hands it back
    // on close) so the Join button never appeared until someone else's discrete action.
    // Only debrid/torrent are (re)joinable: the join endpoint resolves a shared debrid selection or
    // a torrent auto-select. A "file"/onlinestream controller stream has nothing a peer can open, and
    // falling through to the debrid endpoint would kick off an unrelated auto-select (F19).
    const canJoin = !!room?.playbackActive && !!mi && !watchingThis
        && (mi.streamType === "debrid" || mi.streamType === "torrent")

    const join = React.useCallback(() => {
        if (!room?.id || !mi) return
        if (mi.streamType !== "debrid" && mi.streamType !== "torrent") return
        setOptedOut(null)
        if (mi.streamType === "torrent") {
            torrentStart.handleAutoSelectStream({ mediaId: mi.mediaId, episodeNumber: mi.episodeNumber, aniDBEpisode: mi.aniDbEpisode || "" })
        } else {
            joinRoomStream.mutate({
                roomId: room.id,
                clientId: clientId || "",
                playbackType: debridStart.getResolvedPlaybackType(),
                directCdnCapable: __isElectronDesktop__,
            })
        }
    }, [room, mi, clientId, setOptedOut, joinRoomStream, debridStart, torrentStart])

    return { canJoin, join, isPending: joinRoomStream.isPending || debridStart.isPending || torrentStart.isPending }
}
