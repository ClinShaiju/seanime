import { useServerMutation, useServerQuery } from "@/api/client/requests"
import {
    DebridAddTorrents_Variables,
    DebridCancelDownload_Variables,
    DebridCancelStream_Variables,
    DebridDeleteTorrent_Variables,
    DebridDownloadTorrent_Variables,
    DebridGetTorrentFilePreviews_Variables,
    DebridGetTorrentInfo_Variables,
    DebridStartStream_Variables,
    SaveDebridSettings_Variables,
    SaveDummyDebridSettings_Variables,
} from "@/api/generated/endpoint.types"
import { API_ENDPOINTS } from "@/api/generated/endpoints"
import {
    Debrid_TorrentInfo,
    Debrid_TorrentItem,
    DebridClient_FilePreview,
    DebridClient_PrewarmStatusItem,
    Models_DebridSettings,
    Models_DummyDebridSettings,
} from "@/api/generated/types"
import { useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

export function useGetDebridSettings() {
    return useServerQuery<Models_DebridSettings>({
        endpoint: API_ENDPOINTS.DEBRID.GetDebridSettings.endpoint,
        method: API_ENDPOINTS.DEBRID.GetDebridSettings.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.GetDebridSettings.key],
        enabled: true,
    })
}

export function useSaveDebridSettings() {
    const qc = useQueryClient()
    return useServerMutation<Models_DebridSettings, SaveDebridSettings_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.SaveDebridSettings.endpoint,
        method: API_ENDPOINTS.DEBRID.SaveDebridSettings.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.SaveDebridSettings.key],
        onSuccess: async () => {
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.GetDebridSettings.key] })
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.STATUS.GetStatus.key] })
        },
    })
}

export function useGetDummyDebridSettings(enabled: boolean) {
    return useServerQuery<Models_DummyDebridSettings>({
        endpoint: API_ENDPOINTS.DEBRID.GetDummyDebridSettings.endpoint,
        method: API_ENDPOINTS.DEBRID.GetDummyDebridSettings.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.GetDummyDebridSettings.key],
        enabled: enabled,
    })
}

export function useSaveDummyDebridSettings() {
    const qc = useQueryClient()
    return useServerMutation<Models_DummyDebridSettings, SaveDummyDebridSettings_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.SaveDummyDebridSettings.endpoint,
        method: API_ENDPOINTS.DEBRID.SaveDummyDebridSettings.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.SaveDummyDebridSettings.key],
        onSuccess: async () => {
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.GetDummyDebridSettings.key] })
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.STATUS.GetStatus.key] })
        },
    })
}

export function useDebridAddTorrents(onSuccess: () => void) {
    const qc = useQueryClient()
    return useServerMutation<boolean, DebridAddTorrents_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridAddTorrents.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridAddTorrents.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridAddTorrents.key],
        onSuccess: async () => {
            onSuccess()
            toast.success("Torrent added")
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrents.key] })
        },
    })
}

export function useDebridDownloadTorrent() {
    const qc = useQueryClient()
    return useServerMutation<boolean, DebridDownloadTorrent_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridDownloadTorrent.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridDownloadTorrent.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridDownloadTorrent.key],
        onSuccess: async () => {
            toast.info("Download started")
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrents.key] })
        },
    })
}

export function useDebridCancelDownload() {
    const qc = useQueryClient()
    return useServerMutation<boolean, DebridCancelDownload_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridCancelDownload.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridCancelDownload.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridCancelDownload.key],
        onSuccess: async () => {
            toast.info("Download cancelled")
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrents.key] })
        },
    })
}

export function useDebridDeleteTorrent() {
    const qc = useQueryClient()
    return useServerMutation<boolean, DebridDeleteTorrent_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridDeleteTorrent.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridDeleteTorrent.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridDeleteTorrent.key],
        onSuccess: async () => {
            toast.success("Torrent deleted")
            await qc.invalidateQueries({ queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrents.key] })
        },
    })
}

export function useDebridGetTorrents(enabled: boolean, refetchInterval: number) {
    return useServerQuery<Array<Debrid_TorrentItem>>({
        endpoint: API_ENDPOINTS.DEBRID.DebridGetTorrents.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridGetTorrents.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrents.key],
        enabled: enabled,
        retry: 3,
        refetchInterval: refetchInterval,
    })
}

export function useDebridGetTorrentInfo(variables: Partial<DebridGetTorrentInfo_Variables>, enabled: boolean) {
    return useServerQuery<Debrid_TorrentInfo, DebridGetTorrentInfo_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridGetTorrentInfo.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridGetTorrentInfo.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrentInfo.key, variables?.torrent?.infoHash],
        data: variables as DebridGetTorrentInfo_Variables,
        enabled: enabled,
        gcTime: 0,
    })
}

export function useDebridGetTorrentFilePreviews(variables: Partial<DebridGetTorrentFilePreviews_Variables>, enabled: boolean) {
    return useServerQuery<Array<DebridClient_FilePreview>, DebridGetTorrentFilePreviews_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridGetTorrentFilePreviews.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridGetTorrentFilePreviews.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.DebridGetTorrentFilePreviews.key, variables?.torrent?.infoHash],
        data: variables as DebridGetTorrentFilePreviews_Variables,
        enabled: enabled,
        gcTime: 0,
    })
}

export function useDebridStartStream() {
    return useServerMutation<boolean, DebridStartStream_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridStartStream.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridStartStream.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridStartStream.key],
        onSuccess: async () => {
        },
    })
}

// useDebridRefreshStreamUrl re-resolves a fresh CDN link for the caller's active stream.
// Direct-CDN playback calls this when the raw CDN link dies mid-playback (expired token /
// hard 429), then swaps the video src and seeks back.
export function useDebridRefreshStreamUrl() {
    return useServerMutation<string>({
        endpoint: API_ENDPOINTS.DEBRID.DebridRefreshStreamUrl.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridRefreshStreamUrl.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridRefreshStreamUrl.key],
    })
}

export function useDebridCancelStream() {
    return useServerMutation<boolean, DebridCancelStream_Variables>({
        endpoint: API_ENDPOINTS.DEBRID.DebridCancelStream.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridCancelStream.methods[0],
        mutationKey: [API_ENDPOINTS.DEBRID.DebridCancelStream.key],
        onSuccess: async () => {
            toast.success("Stream cancelled")
        },
    })
}

// useDebridPrewarmStatus returns the set of prewarmed episodes for the current user, so the UI can
// badge episodes that will play instantly. Fetched on mount (RQ default staleTime 0 → refetches
// when an episode/library page mounts) and invalidated when a client preload fires, instead of a
// fixed poll. The endpoint returns [] when debrid/preload is off. Pass enabled to avoid fetching
// when debrid is disabled (the caller reads serverStatus).
export function useDebridPrewarmStatus(enabled: boolean) {
    return useServerQuery<Array<DebridClient_PrewarmStatusItem>>({
        endpoint: API_ENDPOINTS.DEBRID.DebridGetPrewarmStatus.endpoint,
        method: API_ENDPOINTS.DEBRID.DebridGetPrewarmStatus.methods[0],
        queryKey: [API_ENDPOINTS.DEBRID.DebridGetPrewarmStatus.key],
        enabled: enabled,
        gcTime: 60_000,
    })
}
