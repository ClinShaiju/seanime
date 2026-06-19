import { HibikeTorrent_AnimeTorrent, HibikeTorrent_BatchEpisodeFiles } from "@/api/generated/types"
import { useDebridStartStream } from "@/api/hooks/debrid.hooks"
import {
    ElectronPlaybackMethod,
    PlaybackTorrentStreaming,
    useCurrentDevicePlaybackSettings,
    useExternalPlayerLink,
} from "@/app/(main)/_atoms/playback.atoms"
import { __debridstream_stateAtom } from "@/app/(main)/entry/_containers/debrid-stream/debrid-stream-overlay"
import { __debridStream_currentSessionAutoSelectAtom } from "@/app/(main)/entry/_containers/debrid-stream/debrid-stream-page"
import { ForcePlaybackMethod, useForcePlaybackMethod } from "@/app/(main)/entry/_lib/handle-play-media"
import { clientIdAtom } from "@/app/websocket-provider"
import { logger } from "@/lib/helpers/debug"
import { __isElectronDesktop__ } from "@/types/constants"
import { useQueryClient } from "@tanstack/react-query"
import { useAtomValue, useSetAtom } from "jotai"
import { useAtom } from "jotai/react"
import React from "react"

type DebridStreamSelectionProps = {
    torrent: HibikeTorrent_AnimeTorrent
    mediaId: number
    episodeNumber: number
    aniDBEpisode: string
    chosenFileId: string
    batchEpisodeFiles: HibikeTorrent_BatchEpisodeFiles | undefined
    forcePlaybackMethod?: ForcePlaybackMethod
    preload?: boolean
}
type DebridStreamAutoSelectProps = {
    mediaId: number
    episodeNumber: number
    aniDBEpisode: string
    forcePlaybackMethod?: ForcePlaybackMethod
    preload?: boolean
}

export function useHandleStartDebridStream() {

    const { mutate, isPending } = useDebridStartStream()
    const qc = useQueryClient()

    const { torrentStreamingPlayback, electronPlaybackMethod } = useCurrentDevicePlaybackSettings()
    const { externalPlayerLink } = useExternalPlayerLink()
    const clientId = useAtomValue(clientIdAtom)

    const setCurrentSessionAutoSelect = useSetAtom(__debridStream_currentSessionAutoSelectAtom)

    const [state, setState] = useAtom(__debridstream_stateAtom)

    const { resetForcePlaybackMethod, getForcePlaybackMethod } = useForcePlaybackMethod()

    const getPlaybackType = React.useCallback((forcePlaybackMethod?: ForcePlaybackMethod) => {
        if (
            (!forcePlaybackMethod && __isElectronDesktop__ && electronPlaybackMethod === ElectronPlaybackMethod.NativePlayer) ||
            (forcePlaybackMethod && forcePlaybackMethod === "nativeplayer")
        ) {
            return "nativeplayer"
        }
        if (!!externalPlayerLink?.length && (
            (!forcePlaybackMethod && torrentStreamingPlayback === PlaybackTorrentStreaming.ExternalPlayerLink) ||
            (forcePlaybackMethod && forcePlaybackMethod === "externalPlayerLink")
        )) {
            return "externalPlayerLink"
        }
        return "default"
    }, [externalPlayerLink, torrentStreamingPlayback, electronPlaybackMethod])

    const handleStreamSelection = (params: DebridStreamSelectionProps) => {
        const forcePlaybackMethod = getForcePlaybackMethod()
        resetForcePlaybackMethod()
        logger("DEBRID STREAM SELECTION").info("Starting debrid stream", params, getPlaybackType(forcePlaybackMethod))
        mutate({
            mediaId: params.mediaId,
            episodeNumber: params.episodeNumber,
            torrent: params.torrent,
            aniDBEpisode: params.aniDBEpisode,
            fileId: params.chosenFileId,
            playbackType: getPlaybackType(forcePlaybackMethod),
            clientId: clientId || "",
            autoSelect: false,
            batchEpisodeFiles: params.batchEpisodeFiles,
            preload: params.preload,
        }, {
            onSuccess: () => {
            },
            onError: () => {
                // A preload failure must not disturb the episode currently playing.
                if (params.preload) return
                setState(null)
            },
        })
    }

    const handleAutoSelectStream = (params: DebridStreamAutoSelectProps) => {
        const forcePlaybackMethod = getForcePlaybackMethod()
        resetForcePlaybackMethod()
        logger("DEBRID STREAM SELECTION").info("Starting debrid stream (auto select)", params, getPlaybackType(forcePlaybackMethod))
        mutate({
            mediaId: params.mediaId,
            episodeNumber: params.episodeNumber,
            torrent: undefined,
            aniDBEpisode: params.aniDBEpisode,
            fileId: "",
            playbackType: getPlaybackType(forcePlaybackMethod),
            clientId: clientId || "",
            autoSelect: true,
            preload: params.preload,
        }, {
            onSuccess: () => {
            },
            onError: () => {
                // A preload failure must not disturb the episode currently playing
                // (and must not disable auto-select for the real next-episode start).
                if (params.preload) return
                setState(null)
                React.startTransition(() => {
                    setCurrentSessionAutoSelect(false)
                })
            },
        })
    }

    return {
        isUsingNativePlayer: __isElectronDesktop__ && electronPlaybackMethod === ElectronPlaybackMethod.NativePlayer,
        handleStreamSelection,
        handleAutoSelectStream,
        isPending,
    }
}
