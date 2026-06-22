package debrid_client

import (
	"context"
	"fmt"
	"seanime/internal/api/anilist"
	"seanime/internal/database/db_bridge"
	"seanime/internal/debrid/debrid"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	torrentanalyzer "seanime/internal/torrents/analyzer"
	"seanime/internal/torrents/autoselect"
	"seanime/internal/util"
	"strconv"

	"github.com/samber/lo"
)

type (
	playbackTorrent struct {
		torrent  *hibiketorrent.AnimeTorrent
		fileId   string
		filepath string
		// streamUrl is set for pre-resolved direct streams (StreamUrl on the result). When
		// non-empty, startStream skips AddTorrent/GetTorrentStreamUrl and plays it directly.
		streamUrl string
	}
)

// resolveAutoSelectProfile returns the auto-select profile to use for a debrid
// stream. A user who turned OFF "use server default auto-select" gets a profile
// from their own preferred resolution; otherwise the server's auto-select profile
// (DB profile, else server preferred resolution, else 1080p) is used.
func (r *Repository) resolveAutoSelectProfile(userID uint) *anime.AutoSelectProfile {
	// Custom mode: the user turned OFF "use server default auto-select" → use their own
	// full auto-select profile.
	if userID != 0 && r.db != nil {
		if ov, _ := r.db.GetUserOverrides(userID); ov != nil && !ov.UseServerDebridAutoSelect {
			if profile, found := db_bridge.FindAutoSelectProfile(r.db, userID); found {
				return profile
			}
			// custom mode but no profile saved yet → fall through to the server default
		}
	}
	// Server default = the admin's profile.
	if profile, found := db_bridge.GetServerAutoSelectProfile(r.db); found {
		return profile
	}
	resolution := "1080p"
	if r.settings != nil && r.settings.StreamPreferredResolution != "" {
		resolution = r.settings.StreamPreferredResolution
	}
	return &anime.AutoSelectProfile{Resolutions: []string{resolution}, MinSeeders: 0}
}

func (r *Repository) findBestTorrent(provider debrid.Provider, media *anilist.CompleteAnime, episodeNumber int, userID uint) (ret *playbackTorrent, err error) {

	defer util.HandlePanicInModuleWithError("debridstream/findBestTorrent", &err)

	r.logger.Debug().Msgf("debridstream: Finding best torrent for %s, Episode %d", media.GetTitleSafe(), episodeNumber)

	profile := r.resolveAutoSelectProfile(userID)

	providerID := provider.GetSettings().ID

	// Prioritize cached torrents. Cache status is resolved in two steps:
	//  1. From a flag the source embedded in the torrent name (free, no network). This is
	//     also the only signal for RealDebrid/AllDebrid (their availability APIs are dead).
	//  2. Provider instant-availability API, but only for torrents whose name had no
	//     recognizable flag — so a fully-flagged source (e.g. AIOStreams) skips the call.
	postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*autoselect.TorrentWithCacheStatus {
		if len(torrents) == 0 {
			return []*autoselect.TorrentWithCacheStatus{}
		}

		cacheKey := func(t *hibiketorrent.AnimeTorrent) string {
			return t.Identity()
		}

		cached := make(map[string]bool, len(torrents))
		unknownHashes := make([]string, 0)
		for _, t := range torrents {
			if t.StreamUrl != "" {
				// Pre-resolved direct stream — implicitly cached, no infohash to look up.
				cached[cacheKey(t)] = true
			} else if isCached, known := parseDebridCacheFlag(t.Name, providerID); known {
				cached[cacheKey(t)] = isCached
			} else if t.InfoHash != "" {
				unknownHashes = append(unknownHashes, t.InfoHash)
			}
		}

		// Only query the API for torrents we couldn't resolve from the name.
		if len(unknownHashes) > 0 {
			instantAvail := provider.GetInstantAvailability(unknownHashes)
			for h := range instantAvail {
				cached[h] = true
			}
		}

		result := make([]*autoselect.TorrentWithCacheStatus, 0, len(torrents))
		for _, t := range torrents {
			result = append(result, &autoselect.TorrentWithCacheStatus{
				Torrent:  t,
				IsCached: cached[cacheKey(t)],
			})
		}

		return result
	}

	result, err := r.autoSelect.FindBestTorrent(
		context.Background(),
		media,
		episodeNumber,
		profile,
		autoselect.SelectionModeDebrid,
		postSearchSort,
		nil,
		provider,
	)
	if err != nil {
		r.logger.Error().Err(err).Msg("debridstream: Auto-select failed")
		if err.Error() == "no torrents found" {
			return nil, fmt.Errorf("no torrents found, please select manually")
		}
		return nil, err
	}

	// Pre-resolved direct stream: no debrid torrent / file analysis, just play the URL.
	if result.OriginalTorrent != nil && result.OriginalTorrent.StreamUrl != "" {
		r.logger.Info().Msgf("debridstream: Auto-selected direct stream: %s", result.OriginalTorrent.Name)
		return &playbackTorrent{
			torrent:   result.OriginalTorrent,
			streamUrl: result.OriginalTorrent.StreamUrl,
			filepath:  result.OriginalTorrent.Name, // filename hint only; episode is resolved from metadata
		}, nil
	}

	if result.DebridTorrent == nil {
		return nil, fmt.Errorf("failed to find torrent")
	}

	// Log success
	r.logger.Info().Msgf("debridstream: Auto-selected torrent: %s", result.OriginalTorrent.Name)
	r.logger.Debug().Msgf("debridstream: Selected file ID: %s", result.DebridFileID)

	ret = &playbackTorrent{
		torrent:  result.OriginalTorrent,
		fileId:   result.DebridFileID,
		filepath: result.AnalysisFile.GetPath(),
	}

	return ret, nil
}

// RankTorrentsForDisplay orders search results for the manual debrid-stream selection screen
// using the auto-select profile scoring, season match, and cache prioritization — without
// dropping any. It layers the source's embedded cache flags on top of the already-fetched
// instant-availability map (no extra API call). Returns the ordered torrents and the set of
// cached infohashes so the UI can badge them (covers RealDebrid/AllDebrid/flagged sources
// whose availability the API can't report).
func (r *Repository) RankTorrentsForDisplay(
	media *anilist.BaseAnime,
	episodeNumber int,
	torrents []*hibiketorrent.AnimeTorrent,
	instantAvail map[string]debrid.TorrentItemInstantAvailability,
) (ordered []*hibiketorrent.AnimeTorrent, cachedHashes map[string]struct{}) {

	cachedHashes = make(map[string]struct{})
	if len(torrents) == 0 {
		return torrents, cachedHashes
	}

	providerID := ""
	if provider, err := r.GetProvider(); err == nil {
		providerID = provider.GetSettings().ID
	}

	statuses := make([]*autoselect.TorrentWithCacheStatus, 0, len(torrents))
	for _, t := range torrents {
		cached := false
		if t.StreamUrl != "" {
			cached = true // pre-resolved direct stream is implicitly cached
		} else if isCached, known := parseDebridCacheFlag(t.Name, providerID); known {
			cached = isCached
		} else if t.InfoHash != "" {
			_, cached = instantAvail[t.InfoHash]
		}
		statuses = append(statuses, &autoselect.TorrentWithCacheStatus{Torrent: t, IsCached: cached})
		if cached && t.InfoHash != "" {
			cachedHashes[t.InfoHash] = struct{}{}
		}
	}

	profile, found := db_bridge.GetServerAutoSelectProfile(r.db)
	if !found {
		resolution := "1080p"
		if r.settings != nil && r.settings.StreamPreferredResolution != "" {
			resolution = r.settings.StreamPreferredResolution
		}
		profile = &anime.AutoSelectProfile{Resolutions: []string{resolution}, MinSeeders: 0}
	}

	// statuses already cover the full set; return them regardless of the slice passed in.
	postSearchSort := func(_ []*hibiketorrent.AnimeTorrent) []*autoselect.TorrentWithCacheStatus {
		return statuses
	}

	ordered = r.autoSelect.Rank(torrents, profile, media.GetPossibleSeasonNumber(), episodeNumber, postSearchSort)
	return ordered, cachedHashes
}

// findBestTorrentFromManualSelection is like findBestTorrent but for a pre-selected torrent
func (r *Repository) findBestTorrentFromManualSelection(provider debrid.Provider, t *hibiketorrent.AnimeTorrent, media *anilist.CompleteAnime, episodeNumber int, chosenFileIndex *int) (ret *playbackTorrent, err error) {

	r.logger.Debug().Msgf("debridstream: Analyzing torrent from %s for %s", t.Link, media.GetTitleSafe())

	// Check if the torrent is cached
	if t.InfoHash != "" {
		instantAvail := provider.GetInstantAvailability([]string{t.InfoHash})
		if len(instantAvail) == 0 {
			r.logger.Warn().Msg("debridstream: Torrent is not cached")
			// We'll still continue since the user specifically selected this torrent
		}
	}

	// Get the magnet link
	magnet, err := r.torrentRepository.ResolveMagnetLink(t)
	if err != nil {
		r.logger.Error().Err(err).Msgf("debridstream: Error scraping magnet link for %s", t.Link)
		return nil, fmt.Errorf("could not get magnet link from %s", t.Link)
	}

	// Set the magnet link
	t.MagnetLink = magnet

	// Get the torrent info from the debrid provider
	info, err := provider.GetTorrentInfo(debrid.GetTorrentInfoOptions{
		MagnetLink: t.MagnetLink,
		InfoHash:   t.InfoHash,
	})
	if err != nil {
		r.logger.Error().Err(err).Msgf("debridstream: Error adding torrent %s", t.Link)
		return nil, err
	}

	// If the torrent has only one file, return it
	if len(info.Files) == 1 {
		return &playbackTorrent{torrent: t, fileId: info.Files[0].ID, filepath: info.Files[0].Path}, nil
	}

	var fileIndex int

	// If the file index is already selected
	if chosenFileIndex != nil {
		fileIndex = *chosenFileIndex
	} else {
		// We know the torrent has multiple files, so we'll need to analyze it
		filepaths := lo.Map(info.Files, func(f *debrid.TorrentItemFile, _ int) string {
			return f.Path
		})

		if len(filepaths) == 0 {
			r.logger.Error().Msg("debridstream: No files found in the torrent")
			return nil, fmt.Errorf("no files found in the torrent")
		}

		// Create a new Torrent Analyzer
		analyzer := torrentanalyzer.NewAnalyzer(&torrentanalyzer.NewAnalyzerOptions{
			Logger:              r.logger,
			Filepaths:           filepaths,
			Media:               media,
			PlatformRef:         r.platformRef,
			MetadataProviderRef: r.metadataProviderRef,
			ForceMatch:          true,
		})

		// Analyze torrent files
		analysis, err := analyzer.AnalyzeTorrentFiles()
		if err != nil {
			r.logger.Warn().Err(err).Msg("debridstream: Error analyzing torrent files")
			return nil, err
		}

		epStr := strconv.Itoa(episodeNumber)
		analysisFile, found := analysis.GetFileByAniDBEpisode(epStr)

		// Multi-cour / multi-season batch: force-match collapses every file onto the
		// requested cour, so more than one file ends up claiming this episode number
		// (e.g. cour 1's ep 1 and cour 2's ep 1 in a full-season batch). The simple
		// lookup then returns the wrong cour. Re-resolve with a media-tree analysis
		// (no force) that assigns each file its true season, and pick by media id.
		if (found && analysis.CountByAniDBEpisode(epStr) > 1) || !found {
			treeAnalyzer := torrentanalyzer.NewAnalyzer(&torrentanalyzer.NewAnalyzerOptions{
				Logger:              r.logger,
				Filepaths:           filepaths,
				Media:               media,
				PlatformRef:         r.platformRef,
				MetadataProviderRef: r.metadataProviderRef,
				ForceMatch:          false,
			})
			if treeAnalysis, e := treeAnalyzer.AnalyzeTorrentFiles(); e == nil {
				if f, ok := treeAnalysis.GetFileByMediaIdAndAniDBEpisode(media.GetID(), epStr); ok {
					analysisFile, found = f, true
					r.logger.Debug().Msgf("debridstream: Resolved cour episode %s for media %d via media-tree analysis", epStr, media.GetID())
				}
			}
		}

		// Check if analyzer found the episode
		if !found {
			r.logger.Error().Msgf("debridstream: Failed to auto-select episode from torrent %s", t.Name)
			return nil, fmt.Errorf("could not find episode %d in torrent", episodeNumber)
		}

		r.logger.Debug().Msgf("debridstream: Found corresponding file for episode %s: %s", strconv.Itoa(episodeNumber), analysisFile.GetLocalFile().Name)

		fileIndex = analysisFile.GetIndex()
	}

	tFile := info.Files[fileIndex]
	r.logger.Debug().Str("file", util.SpewT(tFile)).Msgf("debridstream: Selected file %s", tFile.Name)
	r.logger.Debug().Msgf("debridstream: Selected torrent %s", t.Name)

	return &playbackTorrent{torrent: t, fileId: tFile.ID, filepath: tFile.Path}, nil
}
