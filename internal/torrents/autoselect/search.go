package autoselect

import (
	"context"
	"fmt"
	"seanime/internal/api/anilist"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	itorrent "seanime/internal/torrents/torrent"
	"seanime/internal/util"
	"slices"
	"sync"
	"time"

	"github.com/samber/lo"
)

func (s *AutoSelect) Search(ctx context.Context, media *anilist.BaseAnime, episodeNumber int, profile *anime.AutoSelectProfile) ([]*hibiketorrent.AnimeTorrent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if media == nil {
		return nil, fmt.Errorf("media cannot be nil")
	}
	return s.search(ctx, media.ToCompleteAnime(), episodeNumber, profile)
}

func (s *AutoSelect) search(ctx context.Context, media *anilist.CompleteAnime, episodeNumber int, profile *anime.AutoSelectProfile) ([]*hibiketorrent.AnimeTorrent, error) {
	s.log("Starting auto-select search")
	s.logger.Debug().Msgf("autoselect: Searching for episode %d of %s", episodeNumber, media.GetTitleSafe())

	// 1. Get providers to search
	providers := s.getProvidersToSearch(profile)
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers available")
	}

	s.logger.Debug().Strs("providers", providers).Msg("autoselect: Using providers")
	s.log(fmt.Sprintf("Searching with providers: %v", providers))

	// 2. Determine initial batch search capability
	shouldSearchBatch := s.shouldSearchBatch(media)

	// 3. Search concurrently from all providers
	allTorrents, err := s.searchFromProviders(ctx, providers, media, episodeNumber, shouldSearchBatch, profile)
	if err != nil {
		return nil, err
	}

	if len(allTorrents) == 0 {
		s.logger.Warn().Msg("autoselect: No torrents found")
		s.log("No torrents found")
		return nil, fmt.Errorf("no torrents found")
	}

	s.logger.Debug().Int("count", len(allTorrents)).Msg("autoselect: Total unique torrents found")
	s.log(fmt.Sprintf("Total unique torrents: %d", len(allTorrents)))

	return allTorrents, nil
}

// getProvidersToSearch returns the list of providers to search.
func (s *AutoSelect) getProvidersToSearch(profile *anime.AutoSelectProfile) []string {
	// Use profile providers if available
	if profile != nil && len(profile.Providers) > 0 {
		// Take 3 max
		maxProviders := 3
		if len(profile.Providers) < maxProviders {
			maxProviders = len(profile.Providers)
		}
		return profile.Providers[:maxProviders]
	}

	// Fall back to default provider
	defaultProviderExtension, ok := s.torrentRepository.GetDefaultAnimeProviderExtension()
	if !ok {
		s.logger.Error().Msg("autoselect: Default provider extension not found")
		return nil
	}
	return []string{defaultProviderExtension.GetID()}
}

// searchFromProviders searches concurrently from all providers and deduplicates results.
func (s *AutoSelect) searchFromProviders(
	ctx context.Context,
	providers []string,
	media *anilist.CompleteAnime,
	episodeNumber int,
	shouldSearchBatch bool,
	profile *anime.AutoSelectProfile,
) ([]*hibiketorrent.AnimeTorrent, error) {

	type providerResult struct {
		torrents []*hibiketorrent.AnimeTorrent
		err      error
	}

	results := make(chan providerResult, len(providers))
	var wg sync.WaitGroup

	// Search from each provider concurrently
	for _, provider := range providers {
		wg.Add(1)
		go func(providerID string) {
			defer wg.Done()

			torrents, err := s.searchFromProvider(ctx, providerID, media, episodeNumber, shouldSearchBatch, profile)
			results <- providerResult{
				torrents: torrents,
				err:      err,
			}
		}(provider)
	}

	// Close results channel when all searches are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect and deduplicate results
	infohashes := make(map[string]struct{})
	var allTorrents []*hibiketorrent.AnimeTorrent
	var lastErr error

	for result := range results {
		if result.err != nil {
			lastErr = result.err
			continue
		}

		for _, t := range result.torrents {
			// Use Identity() not InfoHash: URL-only debrid results have no infohash and would
			// all collapse to the empty key, dropping every direct-stream result but the first.
			id := t.Identity()
			if _, exists := infohashes[id]; !exists {
				allTorrents = append(allTorrents, t)
				infohashes[id] = struct{}{}
			}
		}
	}

	// If no torrents found from any provider, return the last error
	if len(allTorrents) == 0 && lastErr != nil {
		return nil, lastErr
	}

	return allTorrents, nil
}

// searchFromProvider searches from a single provider with batch/single fallback logic.
func (s *AutoSelect) searchFromProvider(
	ctx context.Context,
	provider string,
	media *anilist.CompleteAnime,
	episodeNumber int,
	shouldSearchBatch bool,
	profile *anime.AutoSelectProfile,
) ([]*hibiketorrent.AnimeTorrent, error) {

	s.logger.Debug().Str("provider", provider).Msg("autoselect: Searching from provider")

	resolutions := []string{""}
	if profile != nil && len(profile.Resolutions) > 0 {
		resolutions = profile.Resolutions
	}

	// Try each resolution until we get results
	for _, resolution := range resolutions {
		if resolution != "" {
			s.logger.Debug().Str("provider", provider).Str("resolution", resolution).Msg("autoselect: Trying resolution")
			s.updateStep(ctx, "searching", fmt.Sprintf("Querying %s for Episode %d [%s]...", provider, episodeNumber, resolution))
		} else {
			s.updateStep(ctx, "searching", fmt.Sprintf("Querying %s for Episode %d...", provider, episodeNumber))
		}

		// Build search options for this resolution
		searchOptions, err := s.buildSearchOptions(provider, media, episodeNumber, shouldSearchBatch, resolution)
		if err != nil {
			s.logger.Warn().Err(err).Str("provider", provider).Msg("autoselect: Failed to build search options")
			continue
		}

		// Search for torrents. For a finished series the engine always runs BOTH
		// a batch and a single-episode search: on a good batch it merges the two,
		// otherwise it falls back to the single. Old code ran them sequentially
		// (~2 round-trips); fire them concurrently instead. Same number of
		// searches, just overlapped — no extra aggregator load.
		var allTorrents []*hibiketorrent.AnimeTorrent

		if searchOptions.Batch {
			batchOpts := searchOptions
			singleOpts := searchOptions
			singleOpts.Batch = false

			var batchData, singleData *itorrent.SearchData
			var batchErr, singleErr error

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				batchData, batchErr = s.torrentRepository.SearchAnime(ctx, batchOpts)
			}()
			go func() {
				defer wg.Done()
				singleData, singleErr = s.torrentRepository.SearchAnime(ctx, singleOpts)
			}()
			wg.Wait()

			// Keep batch results only if the batch is "good" (same gate as the old
			// sequential path); otherwise the single results stand in for it.
			if batchErr == nil && batchData != nil && s.validateBatchResults(batchData.Torrents) {
				s.logger.Debug().Str("provider", provider).Int("count", len(batchData.Torrents)).Msg("autoselect: Found valid batch torrents")
				allTorrents = append(allTorrents, batchData.Torrents...)
			} else {
				s.logger.Warn().Err(batchErr).Str("provider", provider).Msg("autoselect: Batch results insufficient, using single results")
			}

			if singleErr == nil && singleData != nil && len(singleData.Torrents) > 0 {
				s.logger.Debug().Str("provider", provider).Int("count", len(singleData.Torrents)).Msg("autoselect: Found single episode torrents")
				allTorrents = append(allTorrents, singleData.Torrents...)
			}
		} else {
			// Movies / unfinished series: single search only.
			data, err := s.torrentRepository.SearchAnime(ctx, searchOptions)
			if err == nil && data != nil && len(data.Torrents) > 0 {
				allTorrents = append(allTorrents, data.Torrents...)
			}
		}

		// If we found results, return them
		if len(allTorrents) > 0 {
			s.logger.Debug().Str("provider", provider).Str("resolution", resolution).Int("count", len(allTorrents)).Msg("autoselect: Found torrents with resolution")
			return allTorrents, nil
		}

		// no results with this resolution, try next one
		if resolution != "" {
			s.logger.Debug().Str("provider", provider).Str("resolution", resolution).Msg("autoselect: No results with this resolution, trying next")
		}
	}

	// no results found with any resolution
	return nil, fmt.Errorf("no torrents found with any resolution")
}

// shouldSearchBatch determines if we should initially attempt to search for batches.
func (s *AutoSelect) shouldSearchBatch(media *anilist.CompleteAnime) bool {
	if media.IsMovie() || !media.IsFinished() {
		return false
	}

	// Check if 2 weeks have passed since the anime ended
	// This helps avoid unnecessary batch searches for recently ended series to maximize results
	endDate := media.GetEndDate()
	if endDate != nil && endDate.GetYear() != nil && endDate.GetMonth() != nil && endDate.GetDay() != nil {
		endTime := time.Date(*endDate.GetYear(), time.Month(*endDate.GetMonth()), *endDate.GetDay(), 0, 0, 0, 0, time.UTC)
		twoWeeksAgo := time.Now().UTC().AddDate(0, 0, -14)

		if endTime.After(twoWeeksAgo) {
			return false
		}
	}

	return true
}

// buildSearchOptions constructs the search options based on the provider capabilities and resolution.
func (s *AutoSelect) buildSearchOptions(
	provider string,
	media *anilist.CompleteAnime,
	episodeNumber int,
	batch bool,
	resolution string,
) (itorrent.AnimeSearchOptions, error) {

	ext, ok := s.torrentRepository.GetAnimeProviderExtension(provider)
	if !ok {
		return itorrent.AnimeSearchOptions{}, fmt.Errorf("provider %s not found", provider)
	}

	settings := ext.GetProvider().GetSettings()

	searchType := itorrent.AnimeSearchTypeSmart
	query := ""

	if !settings.CanSmartSearch {
		searchType = itorrent.AnimeSearchTypeSimple
		// Use sanitized romaji title for simple search
		query = util.CleanMediaTitle(media.ToBaseAnime().GetRomajiTitleSafe())
	}

	return itorrent.AnimeSearchOptions{
		Provider:      provider,
		Type:          searchType,
		Media:         media.ToBaseAnime(),
		Query:         query,
		Batch:         batch,
		EpisodeNumber: episodeNumber,
		BestReleases:  false,
		Resolution:    resolution,
		SkipPreviews:  true,
	}, nil
}

// validateBatchResults checks if the batch results are sufficient.
func (s *AutoSelect) validateBatchResults(torrents []*hibiketorrent.AnimeTorrent) bool {
	nbFound := len(torrents)
	seedersArr := lo.Map(torrents, func(t *hibiketorrent.AnimeTorrent, _ int) int {
		return t.Seeders
	})

	if len(seedersArr) == 0 {
		return false
	}

	maxSeeders := slices.Max(seedersArr)
	// Conditions for a "good" batch search result
	if maxSeeders >= 15 || nbFound > 2 {
		return true
	}
	return false
}
