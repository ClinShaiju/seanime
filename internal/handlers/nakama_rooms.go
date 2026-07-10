package handlers

import (
	"errors"
	"net/http"
	debrid_client "seanime/internal/debrid/client"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/nakama"
	"time"

	"github.com/labstack/echo/v4"
)

// Same-instance watch rooms (see internal/nakama/watch_room.go + repo nakama-room.md).
// These are distinct from the relay "/room/*" endpoints, which manage the upstream
// Seanime Rooms relay for the peer/host federation party.

// nakamaPoolUser builds the pool identity for the acting request from the profile/
// identity layer. Local source; external users are constructed server-side when an
// external instance contributes them (not wired yet).
func (h *Handler) nakamaPoolUser(c echo.Context) nakama.PoolUser {
	return nakama.PoolUser{
		UserID:   h.CurrentUserID(c),
		Username: h.RequestUsername(c),
		Source:   nakama.PoolSourceLocal,
	}
}

// requireRoomIdentity rejects requests with no resolved user. On a local password-less
// install dataUserID resolves to the admin, so only networked-anon (knows the server
// password, not logged in) is blocked — you need an identity to be in the pool.
func (h *Handler) requireRoomIdentity(c echo.Context) error {
	if h.dataUserID(c) == 0 || h.RequestUsername(c) == "anon" {
		return h.RespondWithStatusError(c, http.StatusForbidden, errors.New("log in to use watch rooms"))
	}
	return nil
}

// HandleNakamaWatchRoomList
//
//	@summary lists available same-instance watch rooms (discovery cards).
//	@route /api/v1/nakama/watch-room/list [GET]
//	@returns []nakama.RoomCard
func (h *Handler) HandleNakamaWatchRoomList(c echo.Context) error {
	cards := h.App.NakamaManager.GetWatchRoomHub().ListRooms()

	// Enrich cards with show title + cover from the anime collection (universal AniList
	// ids; metadata is shared). Best-effort: unresolved media just leaves them empty.
	if len(cards) > 0 {
		if collection, err := h.App.GetAnimeCollection(false); err == nil && collection != nil {
			for _, card := range cards {
				if card.MediaId == 0 {
					continue
				}
				if media, ok := collection.FindAnime(card.MediaId); ok {
					card.Title = media.GetTitleSafe()
					if img := media.GetCoverImageSafe(); img != "" {
						card.CoverImage = img
					}
				}
			}
		}
	}

	return h.RespondWithData(c, cards)
}

// HandleNakamaWatchRoomCreate
//
//	@summary creates a same-instance watch room and joins it as host.
//	@route /api/v1/nakama/watch-room/create [POST]
//	@returns nakama.WatchRoom
func (h *Handler) HandleNakamaWatchRoomCreate(c echo.Context) error {
	type body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		ClientId string `json:"clientId"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.requireRoomIdentity(c); err != nil {
		return err
	}
	b.ClientId = getRequestClientId(c, b.ClientId)

	room, err := h.App.NakamaManager.GetWatchRoomHub().CreateRoom(h.nakamaPoolUser(c), b.ClientId, b.Name, b.Password)
	if err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, room.Snapshot()) // marshal a copy, never the live room (concurrent-map-write crash)
}

// HandleNakamaWatchRoomJoin
//
//	@summary joins a same-instance watch room.
//	@route /api/v1/nakama/watch-room/join [POST]
//	@returns nakama.WatchRoom
func (h *Handler) HandleNakamaWatchRoomJoin(c echo.Context) error {
	type body struct {
		RoomId   string `json:"roomId"`
		Password string `json:"password"`
		ClientId string `json:"clientId"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.requireRoomIdentity(c); err != nil {
		return err
	}
	b.ClientId = getRequestClientId(c, b.ClientId)

	room, err := h.App.NakamaManager.GetWatchRoomHub().JoinRoom(b.RoomId, h.nakamaPoolUser(c), b.ClientId, b.Password)
	if err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, room.Snapshot()) // marshal a copy, never the live room (concurrent-map-write crash)
}

// HandleNakamaWatchRoomLeave
//
//	@summary leaves the current same-instance watch room.
//	@route /api/v1/nakama/watch-room/leave [POST]
//	@returns bool
func (h *Handler) HandleNakamaWatchRoomLeave(c echo.Context) error {
	type body struct {
		RoomId string `json:"roomId"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.App.NakamaManager.GetWatchRoomHub().LeaveRoom(b.RoomId, h.nakamaPoolUser(c).Key()); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleNakamaWatchRoomSetControl
//
//	@summary (host only) grants or revokes playback control for a room member.
//	@route /api/v1/nakama/watch-room/control [POST]
//	@returns bool
func (h *Handler) HandleNakamaWatchRoomSetControl(c echo.Context) error {
	type body struct {
		RoomId     string `json:"roomId"`
		TargetKey  string `json:"targetKey"`
		CanControl bool   `json:"canControl"`
		All        bool   `json:"all"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.App.NakamaManager.GetWatchRoomHub().SetControl(b.RoomId, h.nakamaPoolUser(c).Key(), b.TargetKey, b.CanControl, b.All); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleNakamaWatchRoomForceTracks
//
//	@summary (host only) toggles forcing the host's audio/subtitle tracks on all members.
//	@route /api/v1/nakama/watch-room/force-tracks [POST]
//	@returns bool
func (h *Handler) HandleNakamaWatchRoomForceTracks(c echo.Context) error {
	type body struct {
		RoomId          string `json:"roomId"`
		ForceHostTracks bool   `json:"forceHostTracks"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.App.NakamaManager.GetWatchRoomHub().SetForceHostTracks(b.RoomId, h.nakamaPoolUser(c).Key(), b.ForceHostTracks); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleNakamaWatchRoomAutoSkip
//
//	@summary sets the caller's OP/ED auto-skip vote ("on" | "off" | "auto") for a room.
//	@route /api/v1/nakama/watch-room/autoskip [POST]
//	@returns bool
func (h *Handler) HandleNakamaWatchRoomAutoSkip(c echo.Context) error {
	type body struct {
		RoomId string `json:"roomId"`
		Pref   string `json:"pref"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	if err := h.App.NakamaManager.GetWatchRoomHub().SetAutoSkipPref(b.RoomId, h.nakamaPoolUser(c).Key(), b.Pref); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}

// HandleNakamaWatchRoomJoinStream
//
//	@summary starts (or rejoins) the room's active debrid stream for the caller.
//	@desc Reuses the host's already-resolved debrid SELECTION — no second torrent selection.
//	@desc Retries briefly while the host is still resolving; errors (retryable) rather than
//	@desc auto-selecting independently, which could put this peer on a different release.
//	@route /api/v1/nakama/watch-room/join-stream [POST]
//	@returns bool
func (h *Handler) HandleNakamaWatchRoomJoinStream(c echo.Context) error {
	if err := h.guardStreamingUser(c); err != nil {
		return err
	}
	type body struct {
		RoomId       string                           `json:"roomId"`
		ClientId     string                           `json:"clientId"`
		PlaybackType debrid_client.StreamPlaybackType `json:"playbackType"`
		// DirectCdnCapable: this participant can play a raw CDN link (Denshi). Each capable
		// room member gets its own direct CDN link via the shared-selection dual-link resolve.
		DirectCdnCapable bool `json:"directCdnCapable,omitempty"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	// Membership gate: this endpoint reuses the controller's already-resolved debrid selection,
	// but performs no room-password check of its own (unlike JoinRoom). Require the caller to be
	// an actual participant — otherwise any authenticated user who discovers a roomId (they are
	// broadcast via ListRooms) could piggyback the controller's stream in a room they never joined.
	if !h.App.NakamaManager.GetWatchRoomHub().IsParticipant(b.RoomId, h.nakamaPoolUser(c).Key()) {
		return h.RespondWithStatusError(c, http.StatusForbidden, errors.New("join the room before starting its stream"))
	}

	b.ClientId = getRequestClientId(c, b.ClientId)

	info := h.App.NakamaManager.GetWatchRoomHub().StreamInfo(b.RoomId)
	if !info.Active {
		return h.RespondWithError(c, errors.New("the room has no active stream"))
	}

	opts := &debrid_client.StartStreamOptions{
		MediaId:          info.MediaId,
		EpisodeNumber:    info.EpisodeNumber,
		AniDBEpisode:     info.AniDBEpisode,
		UserAgent:        c.Request().Header.Get("User-Agent"),
		ClientId:         b.ClientId,
		DirectCdnCapable: b.DirectCdnCapable,
		UserID:           h.dataUserID(c),
		PlaybackType:     b.PlaybackType,
	}

	// Reuse the host's SELECTION (already-added debrid torrent item + file) and have this peer
	// resolve its OWN fresh CDN link from it — cheap (no re-search, no createtorrent) and, unlike
	// sharing the host's single resolved link verbatim, peers don't contend on one link (that
	// contention is why a follower's player would open but never load). Falls back to the raw
	// link when there's no torrent item (e.g. a direct-StreamUrl release).
	if info.StreamType == nakama.WatchPartyStreamTypeDebrid {
		// The controller's share is captured at the END of its resolve; a follower reacting to
		// the very first sync can race ahead of it. Retry briefly instead of silently falling
		// back to our own auto-select — an independent selection can pick a DIFFERENT release
		// than the host (different intro timings/duration), which no position sync can fix.
		share, ok := h.App.DebridClientRepository.GetUserStreamShare(info.ControllerUserID)
		for attempt := 0; !ok && attempt < 8; attempt++ {
			select {
			case <-c.Request().Context().Done():
				return h.RespondWithError(c, c.Request().Context().Err())
			case <-time.After(750 * time.Millisecond):
			}
			share, ok = h.App.DebridClientRepository.GetUserStreamShare(info.ControllerUserID)
		}
		if !ok {
			// Still resolving (or the controller's stream is gone) — tell the client to retry
			// via the Join button rather than diverge onto a different release.
			return h.RespondWithError(c, errors.New("the room's stream is not ready yet, try again in a moment"))
		}
		fp := share.Filepath
		if fp == "" {
			fp = "stream.mkv"
		}
		opts.AutoSelect = false
		opts.Torrent = &hibiketorrent.AnimeTorrent{Name: fp}
		if share.TorrentItemId != "" && share.FileId != "" {
			opts.SharedTorrentItemId = share.TorrentItemId
			opts.FileId = share.FileId
		} else {
			opts.Torrent.StreamUrl = share.StreamUrl // no shared item — reuse the raw link
		}
	} else {
		opts.AutoSelect = true
	}

	if err := h.App.DebridClientRepository.StartStream(c.Request().Context(), opts); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}
