package autoselect

import (
	"context"
	"fmt"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata"
	"seanime/internal/api/metadata_provider"
	"seanime/internal/debrid/debrid"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	"seanime/internal/platforms/platform"
	torrent_analyzer "seanime/internal/torrents/analyzer"
	itorrent "seanime/internal/torrents/torrent"
	"seanime/internal/util"

	"github.com/anacrolix/torrent"
	"github.com/rs/zerolog"
)

type SelectionMode string

const (
	SelectionModeTorrent SelectionMode = "torrent"
	SelectionModeDebrid  SelectionMode = "debrid"
)

var (
	ErrNoFileFound = fmt.Errorf("no file found")
)

type (
	AutoSelect struct {
		logger            *zerolog.Logger
		torrentRepository *itorrent.Repository
		metadataProvider  *util.Ref[metadata_provider.Provider]
		platform          *util.Ref[platform.Platform]
		onEvent           func(string)
	}

	NewAutoSelectOptions struct {
		Logger            *zerolog.Logger
		TorrentRepository *itorrent.Repository
		MetadataProvider  *util.Ref[metadata_provider.Provider]
		Platform          *util.Ref[platform.Platform]
		OnEvent           func(string)
	}

	Result struct {
		Torrent         *torrent.Torrent // For torrent client
		File            *torrent.File    // For torrent client
		AnalysisFile    *torrent_analyzer.File
		DebridTorrent   *debrid.TorrentInfo         // For debrid
		DebridFileID    string                      // For debrid
		OriginalTorrent *hibiketorrent.AnimeTorrent // The original torrent object
	}
)

func New(opts *NewAutoSelectOptions) *AutoSelect {
	return &AutoSelect{
		logger:            opts.Logger,
		torrentRepository: opts.TorrentRepository,
		metadataProvider:  opts.MetadataProvider,
		platform:          opts.Platform,
		onEvent:           opts.OnEvent,
	}
}

type TorrentClient interface {
	AddTorrent(ctx context.Context, magnet string) (*torrent.Torrent, error)
	RemoveTorrent(hash string) error
}

type DebridClient interface {
	GetTorrentInfo(opts debrid.GetTorrentInfoOptions) (*debrid.TorrentInfo, error)
}

func (s *AutoSelect) FindBestTorrent(
	ctx context.Context,
	media *anilist.CompleteAnime,
	episodeNumber int,
	profile *anime.AutoSelectProfile,
	mode SelectionMode,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
	torrentClient TorrentClient,
	debridClient debrid.Provider,
) (*Result, error) {

	// 1. Search
	s.log("Searching for torrents")
	torrents, err := s.search(ctx, media, episodeNumber, profile)
	if err != nil {
		s.log(fmt.Sprintf("Search failed: %v", err))
		return nil, err
	}

	// 2. Filter & sort
	s.log("Filtering and sorting candidates")
	expectedSeason := s.ResolveExpectedSeason(media.GetID(), media.GetPossibleSeasonNumber())
	torrents = s.filterAndSort(torrents, profile, expectedSeason, episodeNumber, postSearchSort)

	// 3. Select file (iterate top 3)
	s.log("Selecting best file from top candidates")
	return s.selectFile(ctx, media, episodeNumber, torrents, mode, torrentClient, debridClient)
}

// ResolveExpectedSeason recovers the entry's real season number. AniList titles for sequels
// often carry a unique subtitle and no season number (e.g. "...Santa Claus no Yume" = Bunny
// Girl Senpai S2), so GetPossibleSeasonNumber() returns -1 and the S1-leak gate never fires.
// The metadata provider (animap -> TMDB season) is the same source the UI's season grouping
// uses, so prefer it and fall back to titleSeason (the caller's title-parsed number) only when
// metadata is unavailable. titleSeason lets BaseAnime callers (the UI sort) reuse this without
// a CompleteAnime. The metadata lookup is in-memory cached, so it's free for a viewed entry.
func (s *AutoSelect) ResolveExpectedSeason(mediaId int, titleSeason int) int {
	if s.metadataProvider != nil {
		if p := s.metadataProvider.Get(); p != nil {
			if md, err := p.GetAnimeMetadata(metadata.AnilistPlatform, mediaId); err == nil && md != nil {
				if n := anime.SeasonNumberFromMetadata(md); n >= 1 {
					return n
				}
			}
		}
	}
	return titleSeason
}

func (s *AutoSelect) log(msg string) {
	if s.onEvent != nil {
		s.onEvent(msg)
	}
}
