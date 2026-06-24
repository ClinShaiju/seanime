package handlers

import (
	"errors"
	"net/http"
	debrid_client "seanime/internal/debrid/client"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/nakama"

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
	return h.RespondWithData(c, room)
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
	return h.RespondWithData(c, room)
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
//	@desc Reuses the host's already-resolved debrid link directly — no second torrent
//	@desc selection or CDN resolution. Falls back to auto-select if the host link isn't ready.
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
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}
	b.ClientId = getRequestClientId(c, b.ClientId)

	info := h.App.NakamaManager.GetWatchRoomHub().StreamInfo(b.RoomId)
	if !info.Active {
		return h.RespondWithError(c, errors.New("the room has no active stream"))
	}

	opts := &debrid_client.StartStreamOptions{
		MediaId:       info.MediaId,
		EpisodeNumber: info.EpisodeNumber,
		AniDBEpisode:  info.AniDBEpisode,
		UserAgent:     c.Request().Header.Get("User-Agent"),
		ClientId:      b.ClientId,
		UserID:        h.dataUserID(c),
		PlaybackType:  b.PlaybackType,
	}

	// Share the host's resolved CDN link verbatim: a torrent carrying StreamUrl skips
	// AddTorrent + URL resolution server-side (see debrid stream.go), so the peer plays
	// instantly off the same link — account-agnostic. Fall back to auto-select if the host
	// hasn't resolved yet (or this isn't a debrid room).
	if info.StreamType == nakama.WatchPartyStreamTypeDebrid {
		if url, fp, ok := h.App.DebridClientRepository.GetUserStreamShare(info.ControllerUserID); ok {
			if fp == "" {
				fp = "stream.mkv"
			}
			opts.AutoSelect = false
			opts.Torrent = &hibiketorrent.AnimeTorrent{StreamUrl: url, Name: fp}
		} else {
			opts.AutoSelect = true
		}
	} else {
		opts.AutoSelect = true
	}

	if err := h.App.DebridClientRepository.StartStream(c.Request().Context(), opts); err != nil {
		return h.RespondWithError(c, err)
	}
	return h.RespondWithData(c, true)
}
