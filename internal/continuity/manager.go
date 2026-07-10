package continuity

import (
	"github.com/rs/zerolog"
	"github.com/samber/mo"
	"seanime/internal/database/db"
	"seanime/internal/util/filecache"
	"sync"
	"time"
)

const (
	OnlinestreamKind   Kind = "onlinestream"
	MediastreamKind    Kind = "mediastream"
	ExternalPlayerKind Kind = "external_player"
)

type (
	// Manager is used to manage the user's viewing history across different media types.
	Manager struct {
		fileCacher                  *filecache.Cacher
		db                          *db.Database
		watchHistoryFileCacheBucket *filecache.Bucket
		// lastWatchedFileCacheBucket is a durable per-media "last watched" store. Unlike
		// watchHistoryFileCacheBucket (a resume store that purges completed/barely-started
		// items), this is never purged on read, so it can order the whole library by recency.
		lastWatchedFileCacheBucket *filecache.Bucket

		externalPlayerEpisodeDetails mo.Option[*ExternalPlayerEpisodeDetails]

		// lwSaveThrottle records the last time the durable last-watched store was written per
		// media id, so the 1 Hz status-tick mirror doesn't full-rewrite (+ re-decode for trim)
		// the whole _lw bucket every second. Guarded by mu.
		lwSaveThrottle map[int]time.Time

		logger   *zerolog.Logger
		settings *Settings
		mu       sync.RWMutex
	}

	// ExternalPlayerEpisodeDetails is used to store the episode details when using an external player.
	// Since the media player module only cares about the filepath, the PlaybackManager will store the episode number and media id here when playback starts.
	ExternalPlayerEpisodeDetails struct {
		EpisodeNumber int    `json:"episodeNumber"`
		MediaId       int    `json:"mediaId"`
		Filepath      string `json:"filepath"`
	}

	Settings struct {
		WatchContinuityEnabled bool
	}

	Kind string
)

type (
	NewManagerOptions struct {
		FileCacher *filecache.Cacher
		Logger     *zerolog.Logger
		Database   *db.Database
		// BucketName overrides the watch-history file-cache bucket. Empty = the default
		// (admin / single-user). Per-user sessions pass a user-scoped name so each
		// user's resume positions are isolated.
		BucketName string
	}
)

// NewManager creates a new Manager, it should be initialized once.
func NewManager(opts *NewManagerOptions) *Manager {
	bucketName := WatchHistoryBucketName
	if opts.BucketName != "" {
		bucketName = opts.BucketName
	}
	watchHistoryFileCacheBucket := filecache.NewBucket(bucketName, time.Hour*24*99999)
	// Derive the durable bucket from the resume bucket so per-user isolation is inherited.
	lastWatchedFileCacheBucket := filecache.NewBucket(bucketName+"_lw", time.Hour*24*99999)

	ret := &Manager{
		fileCacher:                  opts.FileCacher,
		logger:                      opts.Logger,
		db:                          opts.Database,
		watchHistoryFileCacheBucket: &watchHistoryFileCacheBucket,
		lastWatchedFileCacheBucket:  &lastWatchedFileCacheBucket,
		settings: &Settings{
			WatchContinuityEnabled: false,
		},
		externalPlayerEpisodeDetails: mo.None[*ExternalPlayerEpisodeDetails](),
		lwSaveThrottle:               make(map[int]time.Time),
	}

	ret.logger.Info().Msg("continuity: Initialized manager")

	return ret
}

// SetSettings should be called after initializing the Manager.
func (m *Manager) SetSettings(settings *Settings) {
	if m == nil || settings == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = settings
}

// GetSettings returns the current settings.
func (m *Manager) GetSettings() *Settings {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (m *Manager) SetExternalPlayerEpisodeDetails(details *ExternalPlayerEpisodeDetails) {
	if m == nil || details == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.externalPlayerEpisodeDetails = mo.Some(details)
}
