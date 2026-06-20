package torrent_analyzer

import (
	"errors"
	"path/filepath"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata_provider"
	"seanime/internal/library/anime"
	"seanime/internal/library/scanner"
	"seanime/internal/platforms/platform"
	"seanime/internal/util"
	"seanime/internal/util/limiter"

	"github.com/rs/zerolog"
	lop "github.com/samber/lo/parallel"
)

type (
	// Analyzer is a service similar to the scanner, but it is used to analyze torrent files.
	// i.e. torrent files instead of local files.
	Analyzer struct {
		files               []*File
		media               *anilist.CompleteAnime
		platformRef         *util.Ref[platform.Platform]
		logger              *zerolog.Logger
		metadataProviderRef *util.Ref[metadata_provider.Provider]
		forceMatch          bool
		shared              *SharedContext
	}

	// SharedContext holds per-media analysis state (media tree + cache) that can be
	// reused across multiple AnalyzeTorrentFiles calls for the same media, avoiding
	// redundant AniList media-tree fetches when checking several torrent candidates.
	SharedContext struct {
		mediaContainer *scanner.MediaContainer
		cache          *anilist.CompleteAnimeCache
	}

	// Analysis contains the results of the analysis.
	Analysis struct {
		files         []*File // Hydrated after scanFiles is called
		selectedFiles []*File // Hydrated after findCorrespondingFiles is called
		media         *anilist.CompleteAnime
	}

	// File represents a torrent file and contains its metadata.
	File struct {
		index     int
		path      string
		localFile *anime.LocalFile
	}
)

type (
	NewAnalyzerOptions struct {
		Logger              *zerolog.Logger
		Filepaths           []string               // Filepath of the torrent files
		Media               *anilist.CompleteAnime // The media to compare the files with
		PlatformRef         *util.Ref[platform.Platform]
		MetadataProviderRef *util.Ref[metadata_provider.Provider]
		// This basically skips the matching process and forces the media ID to be set.
		// Used for the auto-select feature because the media is already known.
		ForceMatch bool
		// Optional: reuse a prebuilt media container across analyses for the same media.
		Shared *SharedContext
	}
)

func NewAnalyzer(opts *NewAnalyzerOptions) *Analyzer {
	files := lop.Map(opts.Filepaths, func(filepath string, idx int) *File {
		return newFile(idx, filepath)
	})
	return &Analyzer{
		files:               files,
		media:               opts.Media,
		platformRef:         opts.PlatformRef,
		logger:              opts.Logger,
		metadataProviderRef: opts.MetadataProviderRef,
		forceMatch:          opts.ForceMatch,
		shared:              opts.Shared,
	}
}

// PrepareSharedContext builds the media container once so it can be shared across
// several AnalyzeTorrentFiles calls for the same media (via NewAnalyzerOptions.Shared).
// Returns nil on failure; callers fall back to per-analysis building.
func PrepareSharedContext(media *anilist.CompleteAnime, platformRef *util.Ref[platform.Platform], logger *zerolog.Logger) *SharedContext {
	if media == nil || platformRef == nil || platformRef.IsAbsent() {
		return nil
	}
	mc, cache, err := buildMediaContainer(media, platformRef)
	if err != nil {
		if logger != nil {
			logger.Warn().Err(err).Msg("torrent analyzer: Failed to prepare shared media container")
		}
		return nil
	}
	return &SharedContext{mediaContainer: mc, cache: cache}
}

// buildMediaContainer fetches the media tree and builds the normalized media container.
func buildMediaContainer(media *anilist.CompleteAnime, platformRef *util.Ref[platform.Platform]) (*scanner.MediaContainer, *anilist.CompleteAnimeCache, error) {
	cache := anilist.NewCompleteAnimeCache()
	rl := limiter.NewAnilistLimiter()
	tree := anilist.NewCompleteAnimeRelationTree()
	if err := media.FetchMediaTree(anilist.FetchMediaTreeAll, platformRef.Get().GetAnilistClient(), rl, tree, cache); err != nil {
		return nil, nil, err
	}
	mc := scanner.NewMediaContainer(&scanner.MediaContainerOptions{
		AllMedia: scanner.NormalizedMediaFromAnilistComplete(tree.Values()),
	})
	return mc, cache, nil
}

// AnalyzeTorrentFiles scans the files and returns an Analysis struct containing methods to get the results.
func (a *Analyzer) AnalyzeTorrentFiles() (*Analysis, error) {
	if a.platformRef.IsAbsent() {
		return nil, errors.New("anilist client wrapper is nil")
	}

	if err := a.scanFiles(); err != nil {
		return nil, err
	}

	analysis := &Analysis{
		files: a.files,
		media: a.media,
	}

	return analysis, nil
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (a *Analysis) GetCorrespondingFiles() map[int]*File {
	ret, _ := a.getCorrespondingFiles(func(f *File) bool {
		return true
	})
	return ret
}

func (a *Analysis) GetCorrespondingMainFiles() map[int]*File {
	ret, _ := a.getCorrespondingFiles(func(f *File) bool {
		return f.localFile.IsMain()
	})
	return ret
}

func (a *Analysis) GetMainFileByEpisode(episodeNumber int) (*File, bool) {
	ret, _ := a.getCorrespondingFiles(func(f *File) bool {
		return f.localFile.IsMain()
	})
	for _, f := range ret {
		if f.localFile.Metadata.Episode == episodeNumber {
			return f, true
		}
	}
	return nil, false
}

func (a *Analysis) GetFileByAniDBEpisode(episode string) (*File, bool) {
	for _, f := range a.files {
		if f.localFile.Metadata.AniDBEpisode == episode {
			return f, true
		}
	}
	return nil, false
}

func (a *Analysis) GetUnselectedFiles() map[int]*File {
	_, uRet := a.getCorrespondingFiles(func(f *File) bool {
		return true
	})
	return uRet
}

func (a *Analysis) getCorrespondingFiles(filter func(f *File) bool) (map[int]*File, map[int]*File) {
	ret := make(map[int]*File)
	uRet := make(map[int]*File)
	for _, af := range a.files {
		if af.localFile.MediaId == a.media.ID {
			if filter(af) {
				ret[af.index] = af
			} else {
				uRet[af.index] = af
			}
		} else {
			uRet[af.index] = af
		}
	}
	return ret, uRet
}

// GetIndices returns the indices of the files.
//
// Example:
//
//	selectedFilesMap := analysis.GetCorrespondingMainFiles()
//	selectedIndices := analysis.GetIndices(selectedFilesMap)
func (a *Analysis) GetIndices(files map[int]*File) []int {
	indices := make([]int, 0)
	for i := range files {
		indices = append(indices, i)
	}
	return indices
}

func (a *Analysis) GetFiles() []*File {
	return a.files
}

// GetUnselectedIndices takes a map of selected files and returns the indices of the unselected files.
//
// Example:
//
//	analysis, _ := analyzer.AnalyzeTorrentFiles()
//	selectedFiles := analysis.GetCorrespondingMainFiles()
//	indicesToRemove := analysis.GetUnselectedIndices(selectedFiles)
func (a *Analysis) GetUnselectedIndices(files map[int]*File) []int {
	indices := make([]int, 0)
	for i := range a.files {
		if _, ok := files[i]; !ok {
			indices = append(indices, i)
		}
	}
	return indices
}

func (f *File) GetLocalFile() *anime.LocalFile {
	return f.localFile
}

func (f *File) GetIndex() int {
	return f.index
}

func (f *File) GetPath() string {
	return f.path
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// scanFiles scans the files and matches them with the media.
func (a *Analyzer) scanFiles() error {

	anilistRateLimiter := limiter.NewAnilistLimiter()

	lfs := a.getLocalFiles() // Extract local files from the Files

	// +---------------------+
	// |   MediaContainer    |
	// +---------------------+

	// Reuse a prebuilt media container/cache when provided (avoids refetching the
	// AniList media tree for every torrent candidate of the same media).
	var mc *scanner.MediaContainer
	var completeAnimeCache *anilist.CompleteAnimeCache
	if a.shared != nil && a.shared.mediaContainer != nil && a.shared.cache != nil {
		mc = a.shared.mediaContainer
		completeAnimeCache = a.shared.cache
	} else {
		completeAnimeCache = anilist.NewCompleteAnimeCache()
		tree := anilist.NewCompleteAnimeRelationTree()
		if err := a.media.FetchMediaTree(anilist.FetchMediaTreeAll, a.platformRef.Get().GetAnilistClient(), anilistRateLimiter, tree, completeAnimeCache); err != nil {
			return err
		}
		mc = scanner.NewMediaContainer(&scanner.MediaContainerOptions{
			AllMedia: scanner.NormalizedMediaFromAnilistComplete(tree.Values()),
		})
	}

	//scanLogger, _ := scanner.NewScanLogger("./logs")

	// +---------------------+
	// |      Matcher        |
	// +---------------------+

	matcher := &scanner.Matcher{
		LocalFiles:        lfs,
		MediaContainer:    mc,
		Logger:            util.NewLogger(),
		ScanLogger:        nil,
		ScanSummaryLogger: nil,
	}

	err := matcher.MatchLocalFilesWithMedia()
	if err != nil {
		return err
	}

	if a.forceMatch {
		for _, lf := range lfs {
			lf.MediaId = a.media.GetID()
		}
	}

	// +---------------------+
	// |    FileHydrator     |
	// +---------------------+

	fh := &scanner.FileHydrator{
		LocalFiles:          lfs,
		AllMedia:            mc.NormalizedMedia,
		CompleteAnimeCache:  completeAnimeCache,
		PlatformRef:         a.platformRef,
		MetadataProviderRef: a.metadataProviderRef,
		AnilistRateLimiter:  anilistRateLimiter,
		Logger:              a.logger,
		ScanLogger:          nil,
		ScanSummaryLogger:   nil,
		ForceMediaId:        map[bool]int{true: a.media.GetID(), false: 0}[a.forceMatch],
	}

	fh.HydrateMetadata()

	for _, af := range a.files {
		for _, lf := range lfs {
			if lf.Path == af.localFile.Path {
				af.localFile = lf // Update the local file in the File
				break
			}
		}
	}

	return nil
}

// newFile creates a new File from a file path.
func newFile(idx int, path string) *File {
	path = filepath.ToSlash(path)

	return &File{
		index:     idx,
		path:      path,
		localFile: anime.NewLocalFile(path, ""),
	}
}

func (a *Analyzer) getLocalFiles() []*anime.LocalFile {
	files := make([]*anime.LocalFile, len(a.files))
	for i, f := range a.files {
		files[i] = f.localFile
	}
	return files
}
