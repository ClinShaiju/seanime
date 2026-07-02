package handlers

import (
	"errors"
	"path/filepath"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata"
	"seanime/internal/database/models"
	debrid_client "seanime/internal/debrid/client"
	"seanime/internal/debrid/debrid"
	"seanime/internal/events"
	hibiketorrent "seanime/internal/extension/hibike/torrent"

	"github.com/labstack/echo/v4"
)

// HandleGetDebridSettings
//
//	@summary get debrid settings.
//	@desc This returns the debrid settings.
//	@returns models.DebridSettings
//	@route /api/v1/debrid/settings [GET]
func (h *Handler) HandleGetDebridSettings(c echo.Context) error {
	debridSettings, found := h.App.Database.GetDebridSettings()
	if !found {
		return h.RespondWithError(c, errors.New("debrid settings not found"))
	}

	return h.RespondWithData(c, debridSettings)
}

// HandleSaveDebridSettings
//
//	@summary save debrid settings.
//	@desc This saves the debrid settings.
//	@desc The client should refetch the server status.
//	@returns models.DebridSettings
//	@route /api/v1/debrid/settings [PATCH]
func (h *Handler) HandleSaveDebridSettings(c echo.Context) error {

	type body struct {
		Settings models.DebridSettings `json:"settings"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	settings, err := h.App.Database.UpsertDebridSettings(&b.Settings)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	h.App.InitOrRefreshDebridSettings()

	return h.RespondWithData(c, settings)
}

// HandleDebridAddTorrents
//
//	@summary add torrent to debrid.
//	@desc This adds a torrent to the debrid service.
//	@returns bool
//	@route /api/v1/debrid/torrents [POST]
func (h *Handler) HandleDebridAddTorrents(c echo.Context) error {

	type body struct {
		Torrents    []hibiketorrent.AnimeTorrent `json:"torrents"`
		Media       *anilist.BaseAnime           `json:"media"`
		Destination string                       `json:"destination"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if err := h.guardStrictLocalOnlyAction(c); err != nil {
		return err
	}

	if err := h.guardStrictFilesystemPath(c, b.Destination); err != nil {
		return err
	}

	if !h.App.DebridClientRepository.HasProvider() {
		return h.RespondWithError(c, errors.New("debrid provider not set"))
	}

	for _, torrent := range b.Torrents {
		magnet, err := h.App.TorrentRepository.ResolveMagnetLink(&torrent)
		if err != nil {
			if len(b.Torrents) == 1 {
				return h.RespondWithError(c, err)
			} else {
				h.App.Logger.Err(err).Msg("debrid: Failed to get magnet link")
				h.App.WSEventManager.SendEvent(events.ErrorToast, err.Error())
				continue
			}
		}

		torrent.MagnetLink = magnet

		// Add the torrent to the debrid service
		_, err = h.App.DebridClientRepository.AddAndQueueTorrent(debrid.AddTorrentOptions{
			MagnetLink:   magnet,
			InfoHash:     torrent.InfoHash,
			SelectFileId: "all",
		}, b.Destination, b.Media.ID)
		if err != nil {
			// If there is only one torrent, return the error
			if len(b.Torrents) == 1 {
				return h.RespondWithError(c, err)
			} else {
				// If there are multiple torrents, send an error toast and continue to the next torrent
				h.App.Logger.Err(err).Msg("debrid: Failed to add torrent to debrid")
				h.App.WSEventManager.SendEvent(events.ErrorToast, err.Error())
				continue
			}
		}
	}

	return h.RespondWithData(c, true)
}

// HandleDebridDownloadTorrent
//
//	@summary download torrent from debrid.
//	@desc Manually downloads a torrent from the debrid service locally.
//	@returns bool
//	@route /api/v1/debrid/torrents/download [POST]
func (h *Handler) HandleDebridDownloadTorrent(c echo.Context) error {

	type body struct {
		TorrentItem debrid.TorrentItem `json:"torrentItem"`
		Destination string             `json:"destination"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if err := h.guardStrictLocalOnlyAction(c); err != nil {
		return err
	}

	if err := h.guardStrictFilesystemPath(c, b.Destination); err != nil {
		return err
	}

	if !filepath.IsAbs(b.Destination) {
		return h.RespondWithError(c, errors.New("destination must be an absolute path"))
	}

	if err := h.guardStrictFilesystemPath(c, b.Destination); err != nil {
		return err
	}

	// Download the torrent locally
	err := h.App.DebridClientRepository.DownloadTorrent(b.TorrentItem, b.Destination)
	if err != nil {
		if errors.Is(err, debrid_client.ErrDownloadAlreadyActive) {
			return h.RespondWithData(c, true)
		}
		return h.RespondWithError(c, err)
	}

	// Remove the torrent from the database after the local download starts
	// This prevents the auto downloader from starting a duplicate download
	_ = h.App.Database.DeleteDebridTorrentItemByTorrentItemId(b.TorrentItem.ID)

	return h.RespondWithData(c, true)
}

// HandleDebridCancelDownload
//
//	@summary cancel download from debrid.
//	@desc This cancels a download from the debrid service.
//	@returns bool
//	@route /api/v1/debrid/torrents/cancel [POST]
func (h *Handler) HandleDebridCancelDownload(c echo.Context) error {

	type body struct {
		ItemID string `json:"itemID"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	err := h.App.DebridClientRepository.CancelDownload(b.ItemID)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}

// HandleDebridDeleteTorrent
//
//	@summary remove torrent from debrid.
//	@desc This removes a torrent from the debrid service.
//	@returns bool
//	@route /api/v1/debrid/torrent [DELETE]
func (h *Handler) HandleDebridDeleteTorrent(c echo.Context) error {

	type body struct {
		TorrentItem debrid.TorrentItem `json:"torrentItem"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	provider, err := h.App.DebridClientRepository.GetProvider()
	if err != nil {
		return h.RespondWithError(c, err)
	}

	err = provider.DeleteTorrent(b.TorrentItem.ID)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}

// HandleDebridGetTorrents
//
//	@summary get torrents from debrid.
//	@desc This gets the torrents from the debrid service.
//	@returns []debrid.TorrentItem
//	@route /api/v1/debrid/torrents [GET]
func (h *Handler) HandleDebridGetTorrents(c echo.Context) error {

	provider, err := h.App.DebridClientRepository.GetProvider()
	if err != nil {
		return h.RespondWithError(c, err)
	}

	torrents, err := provider.GetTorrents()
	if err != nil {
		h.App.Logger.Err(err).Msg("debrid: Failed to get torrents")
		return h.RespondWithError(c, err)
	}

	queuedItems, err := h.App.Database.GetDebridTorrentItems()
	if err != nil {
		h.App.Logger.Err(err).Msg("debrid: Failed to get queued torrent items")
		return h.RespondWithError(c, err)
	}

	providerId := provider.GetSettings().ID
	queuedIds := make(map[string]struct{}, len(queuedItems))
	for _, item := range queuedItems {
		if item == nil || (item.Provider != "" && item.Provider != providerId) {
			continue
		}

		queuedIds[item.TorrentItemID] = struct{}{}
	}

	for _, torrent := range torrents {
		if torrent == nil {
			continue
		}

		_, torrent.IsQueuedForLocalDownload = queuedIds[torrent.ID]
		torrent.IsDownloadingLocally = h.App.DebridClientRepository.IsDownloadActive(torrent.ID)
	}

	return h.RespondWithData(c, torrents)
}

// HandleDebridGetTorrentInfo
//
//	@summary get torrent info from debrid.
//	@desc This gets the torrent info from the debrid service.
//	@returns debrid.TorrentInfo
//	@route /api/v1/debrid/torrents/info [POST]
func (h *Handler) HandleDebridGetTorrentInfo(c echo.Context) error {
	type body struct {
		Torrent hibiketorrent.AnimeTorrent `json:"torrent"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	magnet, err := h.App.TorrentRepository.ResolveMagnetLink(&b.Torrent)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	b.Torrent.MagnetLink = magnet

	torrentInfo, err := h.App.DebridClientRepository.GetTorrentInfo(debrid.GetTorrentInfoOptions{
		MagnetLink: b.Torrent.MagnetLink,
		InfoHash:   b.Torrent.InfoHash,
	})
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, torrentInfo)
}

// HandleDebridGetTorrentFilePreviews
//
//	@summary get list of torrent files
//	@returns []debrid_client.FilePreview
//	@route /api/v1/debrid/torrents/file-previews [POST]
func (h *Handler) HandleDebridGetTorrentFilePreviews(c echo.Context) error {
	type body struct {
		Torrent       *hibiketorrent.AnimeTorrent `json:"torrent"`
		EpisodeNumber int                         `json:"episodeNumber"`
		Media         *anilist.BaseAnime          `json:"media"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	magnet, err := h.App.TorrentRepository.ResolveMagnetLink(b.Torrent)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	b.Torrent.MagnetLink = magnet

	// Get the media
	animeMetadata, _ := h.App.MetadataProviderRef.Get().GetAnimeMetadata(metadata.AnilistPlatform, b.Media.ID)
	absoluteOffset := 0
	if animeMetadata != nil {
		absoluteOffset = animeMetadata.GetOffset()
	}

	torrentInfo, err := h.App.DebridClientRepository.GetTorrentFilePreviewsFromManualSelection(&debrid_client.GetTorrentFilePreviewsOptions{
		Torrent:        b.Torrent,
		Magnet:         magnet,
		EpisodeNumber:  b.EpisodeNumber,
		Media:          b.Media,
		AbsoluteOffset: absoluteOffset,
	})
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, torrentInfo)
}

// HandleDebridStartStream
//
//	@summary start stream from debrid.
//	@desc This starts streaming a torrent from the debrid service.
//	@returns bool
//	@route /api/v1/debrid/stream/start [POST]
func (h *Handler) HandleDebridStartStream(c echo.Context) error {
	if err := h.guardStreamingUser(c); err != nil {
		return err
	}
	type body struct {
		MediaId           int                              `json:"mediaId"`
		EpisodeNumber     int                              `json:"episodeNumber"`
		AniDBEpisode      string                           `json:"aniDBEpisode"`
		AutoSelect        bool                             `json:"autoSelect"`
		Torrent           *hibiketorrent.AnimeTorrent      `json:"torrent"`
		FileId            string                           `json:"fileId"`
		FileIndex         *int                             `json:"fileIndex"`
		PlaybackType      debrid_client.StreamPlaybackType `json:"playbackType"` // "default" or "externalPlayerLink"
		ClientId          string                           `json:"clientId"`
		// DirectCdnCapable is sent by clients that can play a raw debrid CDN URL themselves
		// (Denshi/Electron — injects CORS headers). Web tabs send false → proxy.
		DirectCdnCapable bool `json:"directCdnCapable,omitempty"`
		BatchEpisodeFiles *hibiketorrent.BatchEpisodeFiles `json:"batchEpisodeFiles"`
		// Preload is true if the stream should only be resolved and cached, not played.
		Preload bool `json:"preload,omitempty"`
		// PrewarmMetadata, when set on a preload, also warms the MKV metadata/CDN (font
		// attachments, HEAD) so the first frame is instant. Only the @3s next-episode trigger
		// sets this — it's the highest-certainty target. CDN load is bounded by cdnWarmLimiter.
		PrewarmMetadata bool `json:"prewarmMetadata,omitempty"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	b.ClientId = getRequestClientId(c, b.ClientId)

	// Per-user request log (helps attribute streams/mpv launches to a specific user when
	// debugging multi-user playback — e.g. a browser client using the default/external
	// player launches mpv server-side).
	// Log only real (non-preload) starts. The speculative preload layer (hover / entry /
	// next-episode prewarm) fires this endpoint frequently with preload=true; logging those
	// would spam the request log.
	if u := h.CurrentUser(c); u != nil && !b.Preload {
		h.App.Logger.Info().
			Str("user", u.Username).
			Str("playbackType", string(b.PlaybackType)).
			Int("mediaId", b.MediaId).
			Msg("debrid: stream start requested")
	}

	userAgent := c.Request().Header.Get("User-Agent")

	if b.Torrent != nil {
		magnet, err := h.App.TorrentRepository.ResolveMagnetLink(b.Torrent)
		if err != nil {
			return h.RespondWithError(c, err)
		}

		b.Torrent.MagnetLink = magnet
	}

	opts := &debrid_client.StartStreamOptions{
		MediaId:           b.MediaId,
		EpisodeNumber:     b.EpisodeNumber,
		AniDBEpisode:      b.AniDBEpisode,
		Torrent:           b.Torrent,
		FileId:            b.FileId,
		FileIndex:         b.FileIndex,
		UserAgent:         userAgent,
		ClientId:          b.ClientId,
		DirectCdnCapable:  b.DirectCdnCapable,
		UserID:            h.dataUserID(c),
		PlaybackType:      b.PlaybackType,
		AutoSelect:        b.AutoSelect,
		BatchEpisodeFiles: b.BatchEpisodeFiles,
		Preload: b.Preload,
		// Metadata warming (MKV font/attachment download + HEAD) is opt-in per request: only the
		// @3s next-episode trigger sets prewarmMetadata, so the bulk preloads (hover / entry-mount)
		// stay URL-only. Previously this was hardcoded false because firing it on every card burst
		// the CDN into HTTP 429s; that's now bounded by cdnWarmLimiter (directstream), so the one
		// high-certainty target can safely warm its first frame.
		PrewarmMetadata: b.Preload && b.PrewarmMetadata,
	}

	if b.Preload {
		if err := h.App.DebridClientRepository.PreloadStream(c.Request().Context(), opts); err != nil {
			return h.RespondWithError(c, err)
		}
	} else {
		if err := h.App.DebridClientRepository.StartStream(c.Request().Context(), opts); err != nil {
			return h.RespondWithError(c, err)
		}
	}

	return h.RespondWithData(c, true)
}

// HandleDebridCancelStream
//
//	@summary cancel stream from debrid.
//	@desc This cancels a stream from the debrid service.
//	@returns bool
//	@route /api/v1/debrid/stream/cancel [POST]
func (h *Handler) HandleDebridCancelStream(c echo.Context) error {
	type body struct {
		Options *debrid_client.CancelStreamOptions `json:"options"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if b.Options == nil {
		b.Options = &debrid_client.CancelStreamOptions{}
	}
	// Scope the cancel to the requesting user's stream manager (per-user streams).
	b.Options.UserID = h.dataUserID(c)

	h.App.DebridClientRepository.CancelStream(b.Options)

	return h.RespondWithData(c, true)
}

// HandleDebridRefreshStreamUrl
//
//	@summary re-resolves a fresh CDN link for the caller's active stream.
//	@desc Direct-CDN clients call this when their raw CDN link dies mid-playback (expired
//	@desc token / hard 429): the server re-resolves from the stored selection and the client
//	@desc swaps its video src and seeks back. The server's own link is untouched.
//	@returns string
//	@route /api/v1/debrid/stream/refresh-url [POST]
func (h *Handler) HandleDebridRefreshStreamUrl(c echo.Context) error {
	if err := h.guardStreamingUser(c); err != nil {
		return err
	}
	url, err := h.App.DebridClientRepository.RefreshStreamUrl(c.Request().Context(), h.dataUserID(c))
	if err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, url)
}

// HandleDebridGetPrewarmStatus
//
//	@summary returns the set of prewarmed episodes for the current user.
//	@desc Used by the UI to badge episodes that are prewarmed and will play instantly.
//	@desc Read-only; never triggers a resolve. Returns an empty list when debrid/preload is off.
//	@returns []debrid_client.PrewarmStatusItem
//	@route /api/v1/debrid/stream/prewarm-status [GET]
func (h *Handler) HandleDebridGetPrewarmStatus(c echo.Context) error {
	items := h.App.DebridClientRepository.GetPrewarmStatus(h.dataUserID(c))
	return h.RespondWithData(c, items)
}
