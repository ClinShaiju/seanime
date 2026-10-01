package autoselect

import (
	"context"
	"fmt"
	"seanime/internal/api/anilist"
	"seanime/internal/debrid/debrid"
	"seanime/internal/extension"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	torrentanalyzer "seanime/internal/torrents/analyzer"
	"seanime/internal/util"
	"strconv"
	"strings"

	"github.com/anacrolix/torrent"
	"github.com/samber/lo"
)

// infoHashFromMagnet extracts the btih infohash from a magnet URI, or "" if absent.
func infoHashFromMagnet(magnet string) string {
	const marker = "urn:btih:"
	idx := strings.Index(strings.ToLower(magnet), marker)
	if idx == -1 {
		return ""
	}
	h := magnet[idx+len(marker):]
	if cut := strings.IndexAny(h, "&/?"); cut != -1 {
		h = h[:cut]
	}
	return h
}

// ResolveCour places media in its season (see torrentanalyzer.Cour). The earlier cours are the
// chain of TV prequels that resolve to the same season — the season source auto-select ranks
// with — so Dr. Stone New World Part 2 sits after New World (both S3) and the walk stops at
// Stone Wars (S2). TV-season numbering from animap stands in when the chain finds nothing.
func (s *AutoSelect) ResolveCour(ctx context.Context, media *anilist.CompleteAnime) torrentanalyzer.Cour {
	c := torrentanalyzer.Cour{Season: s.ResolveExpectedSeason(media.GetID(), media.GetPossibleSeasonNumber()), Index: 1}
	if c.Season > 0 && s.platform != nil && !s.platform.IsAbsent() {
		cur := media
		for i := 0; i < 8 && cur != nil; i++ {
			prev := tvPrequel(cur)
			if prev == nil || s.ResolveExpectedSeason(prev.GetID(), prev.GetPossibleSeasonNumber()) != c.Season {
				break
			}
			c.Index++
			if eps := prev.GetTotalEpisodeCount(); eps > 0 && c.Offset >= 0 {
				c.Offset += eps
			} else {
				c.Offset = -1 // a cour of unknown length: the continuous number is unknowable
			}
			next, err := s.platform.Get().GetAnimeWithRelations(ctx, prev.GetID())
			if err != nil {
				break
			}
			cur = next
		}
		if c.Offset < 0 {
			c.Offset = 0
		}
	}
	if c.Offset == 0 {
		if tv := s.ResolveTVEpisode(media.GetID(), 1); tv.Episode > 1 {
			c.Offset = tv.Episode - 1
		}
	}
	return c
}

func tvPrequel(media *anilist.CompleteAnime) *anilist.BaseAnime {
	for _, edge := range media.GetRelations().GetEdges() {
		if edge == nil || edge.RelationType == nil || *edge.RelationType != anilist.MediaRelationPrequel || edge.Node == nil || edge.Node.Format == nil {
			continue
		}
		switch *edge.Node.Format {
		case anilist.MediaFormatTv, anilist.MediaFormatTvShort, anilist.MediaFormatOna:
			return edge.Node
		}
	}
	return nil
}

// ResolveEpisodeFile picks the file for episodeNumber out of a force-matched analysis: cour
// numbering first (GetFileForEpisode), then a media-tree analysis (no force, files keep their
// own cour). Not found means skip this torrent — never guess between cours.
func (s *AutoSelect) ResolveEpisodeFile(
	ctx context.Context,
	analysis *torrentanalyzer.Analysis,
	filepaths []string,
	media *anilist.CompleteAnime,
	episodeNumber int,
	shared *torrentanalyzer.SharedContext,
) (*torrentanalyzer.File, bool) {
	cour := s.ResolveCour(ctx, media)
	if f, ok := analysis.GetFileForEpisode(episodeNumber, cour); ok {
		s.logger.Debug().Interface("cour", cour).Msgf("autoselect: Episode %d of media %d is %s", episodeNumber, media.GetID(), f.GetPath())
		return f, true
	}
	epStr := strconv.Itoa(episodeNumber)
	tree, err := torrentanalyzer.NewAnalyzer(&torrentanalyzer.NewAnalyzerOptions{
		Logger:              s.logger,
		Filepaths:           filepaths,
		Media:               media,
		PlatformRef:         s.platform,
		MetadataProviderRef: s.metadataProvider,
		Shared:              shared,
	}).AnalyzeTorrentFiles()
	if err == nil {
		if tf, ok := tree.GetFileByMediaIdAndAniDBEpisode(media.GetID(), epStr); ok {
			s.logger.Debug().Msgf("autoselect: Resolved cour episode %s for media %d via media-tree analysis", epStr, media.GetID())
			return tf, true
		}
	}
	s.logger.Warn().Int("claims", analysis.CountByAniDBEpisode(epStr)).Interface("cour", cour).
		Msgf("autoselect: No file is episode %s of media %d, skipping torrent", epStr, media.GetID())
	return nil, false
}

const (
	MaxTorrentCandidatesToCheck = 3
	MaxAnalyzedTorrents         = 3
)

func (s *AutoSelect) selectFile(
	ctx context.Context,
	media *anilist.CompleteAnime,
	episodeNumber int,
	torrents []*hibiketorrent.AnimeTorrent,
	mode SelectionMode,
	torrentClient TorrentClient,
	debridClient debrid.Provider,
) (*Result, error) {
	// Go through the top torrents
	limit := MaxTorrentCandidatesToCheck
	if len(torrents) < limit {
		limit = len(torrents)
	}

	analyzedCount := 0

	// Build the media container once and share it across all candidate analyses
	// (same media), so we don't refetch the AniList media tree per candidate.
	shared := torrentanalyzer.PrepareSharedContext(media, s.platform, s.logger)

	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		t := torrents[i]

		if analyzedCount >= MaxAnalyzedTorrents {
			break
		}

		s.logger.Debug().Msgf("autoselect: Checking torrent candidate: %s", t.Name)
		s.log(fmt.Sprintf("Checking torrent candidate: %s", t.Name))
		s.updateCandidateStatus(ctx, t.Name, "analyzing")

		providerExt, ok := s.torrentRepository.GetAnimeProviderExtension(t.Provider)
		if !ok {
			s.logger.Warn().Str("provider", t.Provider).Msg("autoselect: Provider not found")
			s.updateCandidateStatus(ctx, t.Name, "skipped")
			continue
		}

		var res *Result
		var err error

		switch mode {
		case SelectionModeDebrid:
			if debridClient != nil {
				res, err = s.selectFileFromDebrid(ctx, media, episodeNumber, t, providerExt, debridClient, shared)
			} else {
				s.logger.Error().Msg("autoselect: Debrid client is nil but mode is Debrid")
				s.updateCandidateStatus(ctx, t.Name, "skipped")
				continue
			}
		case SelectionModeTorrent:
			if torrentClient != nil {
				res, err = s.selectFileFromTorrentClient(ctx, media, episodeNumber, t, providerExt, torrentClient, shared)
			} else {
				s.logger.Error().Msg("autoselect: Torrent client is nil but mode is Torrent")
				s.updateCandidateStatus(ctx, t.Name, "skipped")
				continue
			}
		}

		if err == nil && res != nil {
			s.updateCandidateStatus(ctx, t.Name, "selected")
			return res, nil
		}

		if err != nil {
			s.logger.Warn().Err(err).Msgf("autoselect: Could not select file for %s", t.Name)
		}
		s.updateCandidateStatus(ctx, t.Name, "skipped")

		// Count the analysis attempt if we actually tried
		analyzedCount++
	}

	return nil, ErrNoFileFound
}

func (s *AutoSelect) selectFileFromTorrentClient(
	ctx context.Context,
	media *anilist.CompleteAnime,
	episodeNumber int,
	t *hibiketorrent.AnimeTorrent,
	providerExt extension.AnimeTorrentProviderExtension,
	client TorrentClient,
	shared *torrentanalyzer.SharedContext,
) (res *Result, err error) {
	defer util.HandlePanicInModuleWithError("autoselect/selectFileFromTorrentClient", &err)

	s.logger.Trace().Msgf("autoselect: Getting torrent magnet")
	// ResolveMagnetLink returns an already-embedded magnet (t.MagnetLink/t.Link) before
	// scraping — needed for aggregator providers (AIOStreams/SeaDex) that supply the magnet
	// directly and don't implement GetTorrentMagnetLink. Mirrors manual selection.
	magnet, err := s.torrentRepository.ResolveMagnetLink(t)
	if err != nil {
		s.logger.Warn().Err(err).Msgf("autoselect: Error scraping magnet link for %s", t.Link)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.logger.Debug().Msgf("autoselect: Adding torrent %s from magnet", t.Link)

	addedTorrent, err := client.AddTorrent(ctx, magnet)
	if err != nil {
		s.logger.Warn().Err(err).Msgf("autoselect: Error adding torrent %s", t.Link)
		return nil, err
	}

	// Override magnet link
	t.MagnetLink = magnet

	// Only one file, use it
	if len(addedTorrent.Files()) == 1 {
		tFile := addedTorrent.Files()[0]
		addedTorrent.DownloadAll()
		return &Result{
			Torrent:         addedTorrent,
			File:            tFile,
			OriginalTorrent: t,
		}, nil
	}

	// get file paths
	filepaths := lo.Map(addedTorrent.Files(), func(f *torrent.File, _ int) string {
		return f.DisplayPath()
	})

	if len(filepaths) == 0 {
		return nil, fmt.Errorf("no files found")
	}

	// Remove the torrent
	cancel := func() {
		go func() {
			_ = client.RemoveTorrent(addedTorrent.InfoHash().AsString())
		}()
	}

	analyzer := torrentanalyzer.NewAnalyzer(&torrentanalyzer.NewAnalyzerOptions{
		Logger:              s.logger,
		Filepaths:           filepaths,
		Media:               media,
		PlatformRef:         s.platform,
		MetadataProviderRef: s.metadataProvider,
		ForceMatch:          true,
		Shared:              shared,
	})

	analysis, err := analyzer.AnalyzeTorrentFiles()
	if err != nil {
		cancel()
		return nil, err
	}

	analysisFile, found := s.ResolveEpisodeFile(ctx, analysis, filepaths, media, episodeNumber, shared)
	if !found {
		cancel()
		return nil, fmt.Errorf("episode not found")
	}

	// Download the file and unselect the rest
	for i, f := range addedTorrent.Files() {
		if i != analysisFile.GetIndex() {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
	tFile := addedTorrent.Files()[analysisFile.GetIndex()]
	tFile.Download()

	s.log(fmt.Sprintf("Selected file: %s", tFile.DisplayPath()))

	return &Result{
		Torrent:         addedTorrent,
		File:            tFile,
		AnalysisFile:    analysisFile,
		OriginalTorrent: t,
	}, nil
}

func (s *AutoSelect) selectFileFromDebrid(
	ctx context.Context,
	media *anilist.CompleteAnime,
	episodeNumber int,
	t *hibiketorrent.AnimeTorrent,
	providerExt extension.AnimeTorrentProviderExtension,
	client debrid.Provider,
	shared *torrentanalyzer.SharedContext,
) (*Result, error) {

	// Pre-resolved direct stream: aggregators (AIOStreams) hand back a ready URL with no
	// infohash/magnet. Skip the magnet → GetTorrentInfo → file-analysis pipeline entirely;
	// the result is already episode-specific, so there's no file to disambiguate.
	if t.StreamUrl != "" {
		s.logger.Debug().Msgf("autoselect: Direct stream URL for %s, skipping debrid analysis", t.Name)
		s.log(fmt.Sprintf("Selected direct stream: %s", t.Name))
		return &Result{OriginalTorrent: t}, nil
	}

	s.logger.Trace().Msgf("autoselect: Getting torrent magnet")
	// ResolveMagnetLink returns an already-embedded magnet (t.MagnetLink/t.Link) before
	// scraping — needed for aggregator providers (AIOStreams/SeaDex) that supply the magnet
	// directly and don't implement GetTorrentMagnetLink. Mirrors manual selection.
	magnet, err := s.torrentRepository.ResolveMagnetLink(t)
	if err != nil {
		s.logger.Warn().Err(err).Msgf("autoselect: Error scraping magnet link for %s", t.Link)
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Override magnet link
	t.MagnetLink = magnet

	// Prefer the real btih from the magnet. Aggregator providers (AIOStreams/SeaDex) ship a
	// magnet without a separate infohash, so the search pipeline fills t.InfoHash with the
	// torrent NAME (torrent/search.go) — which TorBox/RealDebrid can't look up, yielding
	// "no file found". AnimeTosho/Nyaa carry a real infohash and were unaffected.
	if h := infoHashFromMagnet(magnet); h != "" {
		t.InfoHash = h
	}

	s.logger.Debug().Msgf("autoselect: Checking debrid info for %s", t.Link)

	info, err := client.GetTorrentInfo(debrid.GetTorrentInfoOptions{
		MagnetLink: magnet,
		InfoHash:   t.InfoHash,
	})
	if err != nil {
		s.logger.Warn().Err(err).Msgf("autoselect: Error getting debrid info %s", t.Link)
		return nil, err
	}

	filepaths := lo.Map(info.Files, func(f *debrid.TorrentItemFile, _ int) string {
		return f.Path
	})

	if len(filepaths) == 0 {
		return nil, fmt.Errorf("no files found")
	}

	analyzer := torrentanalyzer.NewAnalyzer(&torrentanalyzer.NewAnalyzerOptions{
		Logger:              s.logger,
		Filepaths:           filepaths,
		Media:               media,
		PlatformRef:         s.platform,
		MetadataProviderRef: s.metadataProvider,
		ForceMatch:          true,
		Shared:              shared,
	})

	analysis, err := analyzer.AnalyzeTorrentFiles()
	if err != nil {
		return nil, err
	}

	analysisFile, found := s.ResolveEpisodeFile(ctx, analysis, filepaths, media, episodeNumber, shared)
	if !found {
		return nil, fmt.Errorf("episode not found")
	}

	tFile := info.Files[analysisFile.GetIndex()]
	s.logger.Debug().Msgf("autoselect: Selected debrid file %s", tFile.Name)
	s.log(fmt.Sprintf("Selected debrid file: %s", tFile.Name))

	// Map the OTHER episodes in this (batch) torrent to their files. Keyed by the same
	// AniDB-episode normalization the selection above used, and only when exactly one file
	// claims the episode (multi-season batches collide under ForceMatch — skip those).
	otherFiles := make(map[int]*debrid.TorrentItemFile)
	for _, af := range analysis.GetFiles() {
		lf := af.GetLocalFile()
		if lf == nil || !lf.IsMain() {
			continue
		}
		epNum, convErr := strconv.Atoi(lf.Metadata.AniDBEpisode)
		if convErr != nil || epNum <= 0 || epNum == episodeNumber {
			continue
		}
		if analysis.CountByAniDBEpisode(lf.Metadata.AniDBEpisode) != 1 {
			continue
		}
		if af.GetIndex() < len(info.Files) {
			otherFiles[epNum] = info.Files[af.GetIndex()]
		}
	}

	return &Result{
		DebridTorrent:     info,
		DebridFileID:      tFile.ID,
		AnalysisFile:      analysisFile,
		OriginalTorrent:   t,
		OtherEpisodeFiles: otherFiles,
	}, nil
}
