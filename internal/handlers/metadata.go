package handlers

import (
	"errors"
	"seanime/internal/api/anizip"
	"seanime/internal/database/models"
	"seanime/internal/library/anime"
	"seanime/internal/util/filecache"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

var anizipArtworkBucket = filecache.NewBucket("anizip_artwork", 7*24*time.Hour)

// HandlePopulateFillerData
//
//	@summary fetches and caches filler data for the given media.
//	@desc This will fetch and cache filler data for the given media.
//	@returns true
//	@route /api/v1/metadata-provider/filler [POST]
func (h *Handler) HandlePopulateFillerData(c echo.Context) error {
	type body struct {
		MediaId int `json:"mediaId"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	animeCollection, err := h.App.GetAnimeCollection(false)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	media, found := animeCollection.FindAnime(b.MediaId)
	if !found {
		// Fetch media
		media, err = h.App.AnilistPlatformRef.Get().GetAnime(c.Request().Context(), b.MediaId)
		if err != nil {
			return h.RespondWithError(c, err)
		}
	}

	// Fetch filler data
	err = h.App.FillerManager.FetchAndStoreFillerData(b.MediaId, media.GetAllTitlesDeref())
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}

// HandleRemoveFillerData
//
//	@summary removes filler data cache.
//	@desc This will remove the filler data cache for the given media.
//	@returns bool
//	@route /api/v1/metadata-provider/filler [DELETE]
func (h *Handler) HandleRemoveFillerData(c echo.Context) error {
	type body struct {
		MediaId int `json:"mediaId"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	err := h.App.FillerManager.RemoveFillerData(b.MediaId)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}

// HandleGetMediaMetadataParent
//
//	@summary retrieves media metadata parent by media ID.
//	@desc Returns the media metadata parent information for the given media ID.
//	@route /api/v1/metadata/parent/{id} [GET]
//	@param id - int - true - "The media ID"
//	@returns models.MediaMetadataParent
func (h *Handler) HandleGetMediaMetadataParent(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return h.RespondWithError(c, errors.New("invalid id"))
	}

	parent, err := h.App.Database.GetMediaMetadataParent(id)
	if err != nil {
		return h.RespondWithData(c, &models.MediaMetadataParent{})
	}

	return h.RespondWithData(c, parent)
}

// HandleSaveMediaMetadataParent
//
//	@summary saves or updates media metadata parent.
//	@desc Creates or updates the media metadata parent information.
//	@route /api/v1/metadata/parent [POST]
//	@returns models.MediaMetadataParent
func (h *Handler) HandleSaveMediaMetadataParent(c echo.Context) error {
	type body struct {
		MediaId       int `json:"mediaId"`
		ParentId      int `json:"parentId"`
		SpecialOffset int `json:"specialOffset"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if b.MediaId == 0 {
		return h.RespondWithError(c, errors.New("invalid media id"))
	}

	parent := models.MediaMetadataParent{
		MediaId:       b.MediaId,
		ParentId:      b.ParentId,
		SpecialOffset: b.SpecialOffset,
	}

	savedParent, err := h.App.Database.InsertMediaMetadataParent(parent)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	h.App.MetadataProviderRef.Get().ClearCache()
	anime.ClearEpisodeCollectionCache()

	return h.RespondWithData(c, savedParent)
}

// HandleDeleteMediaMetadataParent
//
//	@summary deletes media metadata parent.
//	@desc Removes the media metadata parent information for the given media ID.
//	@route /api/v1/metadata/parent [DELETE]
//	@returns bool
func (h *Handler) HandleDeleteMediaMetadataParent(c echo.Context) error {
	type body struct {
		MediaId int `json:"mediaId"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	err := h.App.Database.DeleteMediaMetadataParent(b.MediaId)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	h.App.MetadataProviderRef.Get().ClearCache()
	anime.ClearEpisodeCollectionCache()

	return h.RespondWithData(c, true)
}

// HandleGetAnizipArtwork
//
//	@summary returns cached artwork URLs (fanart, clearlogo, title) for the loading screen.
//	@route /api/v1/anizip-artwork/{id} [GET]
//	@param id - int - true - "The AniList media ID"
//	@returns anizip.Artwork
func (h *Handler) HandleGetAnizipArtwork(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id == 0 {
		return h.RespondWithError(c, errors.New("invalid id"))
	}

	key := strconv.Itoa(id)

	// Check filecache
	var cached anizip.Artwork
	if found, _ := h.App.FileCacher.Get(anizipArtworkBucket, key, &cached); found {
		return h.RespondWithData(c, &cached)
	}

	// Fetch from ani.zip
	media, err := anizip.FetchAniZipMedia("anilist", id)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	artwork := media.GetArtwork()
	_ = h.App.FileCacher.Set(anizipArtworkBucket, key, artwork)

	return h.RespondWithData(c, artwork)
}
