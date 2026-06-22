package handlers

import (
	"seanime/internal/continuity"
	"strconv"

	"github.com/labstack/echo/v4"
)

// HandleUpdateContinuityWatchHistoryItem
//
//	@summary Updates watch history item.
//	@desc This endpoint is used to update a watch history item.
//	@desc Since this is low priority, we ignore any errors.
//	@route /api/v1/continuity/item [PATCH]
//	@returns bool
func (h *Handler) HandleUpdateContinuityWatchHistoryItem(c echo.Context) error {
	type body struct {
		Options continuity.UpdateWatchHistoryItemOptions `json:"options"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	err := h.userSession(c).Continuity().UpdateWatchHistoryItem(&b.Options)
	if err != nil {
		// Ignore the error
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}

// HandleGetContinuityWatchHistoryItem
//
//	@summary Returns a watch history item.
//	@desc This endpoint is used to retrieve a watch history item.
//	@route /api/v1/continuity/item/{id} [GET]
//	@param id - int - true - "AniList anime media ID"
//	@returns continuity.WatchHistoryItemResponse
func (h *Handler) HandleGetContinuityWatchHistoryItem(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return h.RespondWithError(c, err)
	}

	if !h.userSession(c).Continuity().GetSettings().WatchContinuityEnabled {
		return h.RespondWithData(c, &continuity.WatchHistoryItemResponse{
			Item:  nil,
			Found: false,
		})
	}

	resp := h.userSession(c).Continuity().GetWatchHistoryItem(id)
	return h.RespondWithData(c, resp)
}

// HandleGetContinuityWatchHistory
//
//	@summary Returns the continuity watch history
//	@desc This endpoint is used to retrieve all watch history items.
//	@route /api/v1/continuity/history [GET]
//	@returns continuity.WatchHistory
func (h *Handler) HandleGetContinuityWatchHistory(c echo.Context) error {
	if !h.userSession(c).Continuity().GetSettings().WatchContinuityEnabled {
		ret := make(map[int]*continuity.WatchHistoryItem)
		return h.RespondWithData(c, ret)
	}

	resp := h.userSession(c).Continuity().GetWatchHistory()
	return h.RespondWithData(c, resp)
}
