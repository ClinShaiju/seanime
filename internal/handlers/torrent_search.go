package handlers

import (
	"seanime/internal/api/anilist"
	"seanime/internal/database/db_bridge"
	"seanime/internal/debrid/debrid"
	"seanime/internal/library/anime"
	"seanime/internal/torrents/torrent"
	"seanime/internal/util/result"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
)

var debridInstantAvailabilityCache = result.NewCache[string, map[string]debrid.TorrentItemInstantAvailability]()

// HandleSearchTorrent
//
//	@summary searches torrents and returns a list of torrents and their previews.
//	@desc This will search for torrents and return a list of torrents with previews.
//	@desc If smart search is enabled, it will filter the torrents based on search parameters.
//	@route /api/v1/torrent/search [POST]
//	@returns torrent.SearchData
func (h *Handler) HandleSearchTorrent(c echo.Context) error {

	type body struct {
		// "smart" or "simple"
		Type                    string            `json:"type,omitempty"`
		Provider                string            `json:"provider,omitempty"`
		Query                   string            `json:"query,omitempty"`
		EpisodeNumber           int               `json:"episodeNumber,omitempty"`
		Batch                   bool              `json:"batch,omitempty"`
		Media                   anilist.BaseAnime `json:"media,omitempty"`
		AbsoluteOffset          int               `json:"absoluteOffset,omitempty"`
		Resolution              string            `json:"resolution,omitempty"`
		BestRelease             bool              `json:"bestRelease,omitempty"`
		IncludeSpecialProviders bool              `json:"includeSpecialProviders,omitempty"`
		// When true (debrid-stream selection), results are ordered by the auto-select rules
		// (profile scoring, season match) and cache prioritization, without dropping any.
		SortByAutoSelect bool `json:"sortByAutoSelect,omitempty"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	data, err := h.App.TorrentRepository.SearchAnime(c.Request().Context(), torrent.AnimeSearchOptions{
		Provider:                b.Provider,
		Type:                    torrent.AnimeSearchType(b.Type),
		Media:                   &b.Media,
		Query:                   b.Query,
		Batch:                   b.Batch,
		EpisodeNumber:           b.EpisodeNumber,
		BestReleases:            b.BestRelease,
		Resolution:              b.Resolution,
		IncludeSpecialProviders: b.IncludeSpecialProviders,
	})
	if err != nil {
		return h.RespondWithError(c, err)
	}

	//
	// Debrid torrent instant availability
	//
	if h.App.SecondarySettings.Debrid.Enabled {
		hashes := make([]string, 0)
		for _, t := range data.Torrents {
			if t.InfoHash == "" {
				continue
			}
			hashes = append(hashes, t.InfoHash)
		}
		hashesKey := strings.Join(hashes, ",")
		var found bool
		data.DebridInstantAvailability, found = debridInstantAvailabilityCache.Get(hashesKey)
		if !found {
			provider, err := h.App.DebridClientRepository.GetProvider()
			if err == nil {
				instantAvail := provider.GetInstantAvailability(hashes)
				data.DebridInstantAvailability = instantAvail
				debridInstantAvailabilityCache.Set(hashesKey, instantAvail)
			}
		}

		// Order the manual selection list like the auto-selector would (profile scoring,
		// season match, cached-first), layering source cache flags on top of the API map.
		if b.SortByAutoSelect {
			ordered, cachedHashes := h.App.DebridClientRepository.RankTorrentsForDisplay(&b.Media, b.EpisodeNumber, data.Torrents, data.DebridInstantAvailability)
			data.Torrents = ordered

			if len(data.Previews) > 0 {
				rank := make(map[string]int, len(ordered))
				for i, t := range ordered {
					rank[t.InfoHash] = i
				}
				previewRank := func(p *torrent.Preview) int {
					if p == nil || p.Torrent == nil {
						return 1 << 30
					}
					if rk, ok := rank[p.Torrent.InfoHash]; ok {
						return rk
					}
					return 1 << 30
				}
				sort.SliceStable(data.Previews, func(i, j int) bool {
					return previewRank(data.Previews[i]) < previewRank(data.Previews[j])
				})
			}

			// Surface flag-derived cache (RealDebrid/AllDebrid/flagged sources) as badges.
			if data.DebridInstantAvailability == nil {
				data.DebridInstantAvailability = make(map[string]debrid.TorrentItemInstantAvailability)
			}
			for hash := range cachedHashes {
				if _, ok := data.DebridInstantAvailability[hash]; !ok {
					data.DebridInstantAvailability[hash] = debrid.TorrentItemInstantAvailability{}
				}
			}
		}
	}

	return h.RespondWithData(c, data)
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// HandleGetAutoSelectProfile
//
//	@summary returns the autoselect profile.
//	@desc This returns the single autoselect profile if it exists.
//	@route /api/v1/auto-select/profile [GET]
//	@returns anime.AutoSelectProfile
func (h *Handler) HandleGetAutoSelectProfile(c echo.Context) error {
	profile, err := db_bridge.GetAutoSelectProfile(h.App.Database)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, profile)
}

// HandleSaveAutoSelectProfile
//
//	@summary creates or updates the autoselect profile.
//	@desc Since there's only one profile at all time, this will create or update it.
//	@route /api/v1/auto-select/profile [POST]
//	@returns anime.AutoSelectProfile
func (h *Handler) HandleSaveAutoSelectProfile(c echo.Context) error {
	type body struct {
		Profile *anime.AutoSelectProfile `json:"profile"`
	}

	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	if err := db_bridge.SaveAutoSelectProfile(h.App.Database, b.Profile); err != nil {
		return h.RespondWithError(c, err)
	}

	// Get the saved profile to return it with the DB ID
	profile, err := db_bridge.GetAutoSelectProfile(h.App.Database)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, profile)
}

// HandleDeleteAutoSelectProfile
//
//	@summary deletes the autoselect profile.
//	@route /api/v1/auto-select/profile [DELETE]
//	@returns bool
func (h *Handler) HandleDeleteAutoSelectProfile(c echo.Context) error {
	if err := db_bridge.DeleteAutoSelectProfile(h.App.Database); err != nil {
		return h.RespondWithError(c, err)
	}

	return h.RespondWithData(c, true)
}
