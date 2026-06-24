package debrid_client

import (
	"context"
	"fmt"
	"path/filepath"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata_provider"
	"seanime/internal/database/db"
	"seanime/internal/database/models"
	"seanime/internal/debrid/alldebrid"
	"seanime/internal/debrid/debrid"
	"seanime/internal/debrid/realdebrid"
	"seanime/internal/debrid/torbox"
	"seanime/internal/directstream"
	"seanime/internal/events"
	"seanime/internal/hook"
	"seanime/internal/library/playbackmanager"
	"seanime/internal/platforms/platform"
	"seanime/internal/torrents/autoselect"
	"seanime/internal/torrents/torrent"
	"seanime/internal/util"
	"seanime/internal/util/result"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/mo"
	"golang.org/x/time/rate"
)

var (
	ErrProviderNotSet = fmt.Errorf("debrid: Provider not set")
)

// prewarmKickoffInterval spaces scheduled prewarm resolves so a tick's fan-out doesn't hit
// TorBox simultaneously (see Repository.prewarmLimiter).
const prewarmKickoffInterval = 1500 * time.Millisecond

type (
	Repository struct {
		provider               mo.Option[debrid.Provider]
		logger                 *zerolog.Logger
		db                     *db.Database
		settings               *models.DebridSettings
		wsEventManager         events.WSEventManagerInterface
		ctxMap                 *result.Map[string, context.CancelFunc]
		// queuedDownloadFailures counts consecutive GetTorrent failures per queued item, so
		// the download loop can drop a stale row instead of polling a dead item forever.
		queuedDownloadFailures *result.Map[string, int]
		downloadLoopCancelFunc context.CancelFunc
		torrentRepository      *torrent.Repository
		directStreamManager    *directstream.Manager
		// sessionModulesFunc resolves the per-user DirectStream + PlaybackManager for a
		// stream so multiple users stream independently. nil → fall back to the global
		// (admin) modules above. Injected by core (App.SessionFor based).
		sessionModulesFunc func(userID uint) (*directstream.Manager, *playbackmanager.PlaybackManager)
		// sessionEventsFunc resolves the WS event manager scoped to the streaming user, so
		// a user's stream overlay/loader events reach only them (not always the admin).
		// nil → fall back to wsEventManager (admin-scoped). Injected by core.
		sessionEventsFunc func(userID uint) events.WSEventManagerInterface

		playbackManager *playbackmanager.PlaybackManager
		// streamManagers holds one StreamManager per user. Each owns its own per-stream
		// state (current torrent item, stream URL, download/playback cancel funcs, preload
		// cache), so two users streaming at once can't cancel or overwrite each other's
		// in-flight resolve (the cause of "stuck at downloading 100%" / "player opens then
		// immediately closes" when both start at the same time).
		streamManagers      *result.Map[uint, *StreamManager]
		completeAnimeCache  *anilist.CompleteAnimeCache
		metadataProviderRef *util.Ref[metadata_provider.Provider]
		platformRef         *util.Ref[platform.Platform]

		autoSelect *autoselect.AutoSelect

		// prewarmLimiter spaces the scheduled continue-watching fan-out so a tick's N_users×N
		// targets don't hit TorBox simultaneously (the concurrent burst was a prime 429 source).
		// Client-triggered preloads (play @3s, hover) bypass it — they're individually low-rate.
		prewarmLimiter *rate.Limiter

		// previousStreamOptions is the most-recently-started stream, GLOBAL across users —
		// consumed by single-host features (Nakama host party, plugins) that have one notion
		// of "the current stream". prevOptsMu guards it against concurrent multi-user writes;
		// semantically it's last-writer-wins (the active host/admin stream).
		prevOptsMu            sync.RWMutex
		previousStreamOptions mo.Option[*StartStreamOptions]
	}

	NewRepositoryOptions struct {
		Logger         *zerolog.Logger
		WSEventManager events.WSEventManagerInterface
		Database       *db.Database

		TorrentRepository   *torrent.Repository
		PlaybackManager     *playbackmanager.PlaybackManager
		DirectStreamManager *directstream.Manager
		MetadataProviderRef *util.Ref[metadata_provider.Provider]
		PlatformRef         *util.Ref[platform.Platform]
		// SessionModulesFunc resolves per-user DirectStream + PlaybackManager (optional).
		SessionModulesFunc func(userID uint) (*directstream.Manager, *playbackmanager.PlaybackManager)
		// SessionEventsFunc resolves the WS event manager scoped to a user (optional).
		SessionEventsFunc func(userID uint) events.WSEventManagerInterface
	}
)

func NewRepository(opts *NewRepositoryOptions) (ret *Repository) {
	ret = &Repository{
		provider:       mo.None[debrid.Provider](),
		logger:         opts.Logger,
		wsEventManager: opts.WSEventManager,
		db:             opts.Database,
		settings: &models.DebridSettings{
			Enabled: false,
		},
		torrentRepository:     opts.TorrentRepository,
		platformRef:           opts.PlatformRef,
		playbackManager:       opts.PlaybackManager,
		metadataProviderRef:   opts.MetadataProviderRef,
		completeAnimeCache:    anilist.NewCompleteAnimeCache(),
		ctxMap:                 result.NewMap[string, context.CancelFunc](),
		queuedDownloadFailures: result.NewMap[string, int](),
		previousStreamOptions:  mo.None[*StartStreamOptions](),
		directStreamManager:   opts.DirectStreamManager,
		sessionModulesFunc:    opts.SessionModulesFunc,
		sessionEventsFunc:     opts.SessionEventsFunc,
		streamManagers:        result.NewMap[uint, *StreamManager](),
	}

	ret.autoSelect = autoselect.New(&autoselect.NewAutoSelectOptions{
		Logger:            opts.Logger,
		TorrentRepository: opts.TorrentRepository,
		MetadataProvider:  opts.MetadataProviderRef,
		Platform:          opts.PlatformRef,
	})

	ret.prewarmLimiter = rate.NewLimiter(rate.Every(prewarmKickoffInterval), 1)

	return
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (r *Repository) startOrStopDownloadLoop() {
	// Cancel the previous download loop if it's running
	if r.downloadLoopCancelFunc != nil {
		r.downloadLoopCancelFunc()
	}

	// Start the download loop if the provider is set and enabled
	if r.settings.Enabled && r.provider.IsPresent() {
		ctx, cancel := context.WithCancel(context.Background())
		r.downloadLoopCancelFunc = cancel
		r.launchDownloadLoop(ctx)
	}
}

// InitializeProvider is called each time the settings change
func (r *Repository) InitializeProvider(settings *models.DebridSettings) error {
	// Only drop prewarmed streams when the ACCOUNT changes (provider/key/enabled). A benign settings
	// save (preferred resolution, preload toggle) must NOT cold-start the warm cache and force a
	// re-resolve (+ re-createtorrent) of everything — that was a needless 429/latency source.
	prev := r.settings
	if prev == nil || prev.Provider != settings.Provider || prev.ApiKey != settings.ApiKey || prev.Enabled != settings.Enabled {
		r.ClearAllPreloads()
	}
	r.settings = settings

	if !settings.Enabled {
		r.provider = mo.None[debrid.Provider]()
		// Stop the download loop if it's running
		r.startOrStopDownloadLoop()
		return nil
	}

	switch settings.Provider {
	case "torbox":
		r.provider = mo.Some(torbox.NewTorBox(r.logger))
	case "realdebrid":
		r.provider = mo.Some(realdebrid.NewRealDebrid(r.logger))
	case "alldebrid":
		r.provider = mo.Some(alldebrid.NewAllDebrid(r.logger))
	default:
		r.provider = mo.None[debrid.Provider]()
	}

	if r.provider.IsAbsent() {
		r.logger.Warn().Str("provider", settings.Provider).Msg("debrid: No provider set")
		// Stop the download loop if it's running
		r.startOrStopDownloadLoop()
		return nil
	}

	// Authenticate the provider
	err := r.provider.MustGet().Authenticate(r.settings.ApiKey)
	if err != nil {
		r.logger.Err(err).Msg("debrid: Failed to authenticate")
		r.provider = mo.None[debrid.Provider]()
		// Cancel the download loop if it's running
		if r.downloadLoopCancelFunc != nil {
			r.downloadLoopCancelFunc()
		}
		return err
	}

	// Start the download loop
	r.startOrStopDownloadLoop()

	return nil
}

// usernameFor resolves a userID to a username for logging (per-user attribution).
func (r *Repository) usernameFor(userID uint) string {
	if userID == 0 {
		return "anon"
	}
	if u, err := r.db.GetUserByID(userID); err == nil && u != nil {
		return u.Username
	}
	return fmt.Sprintf("u%d", userID)
}

func (r *Repository) GetProvider() (debrid.Provider, error) {
	p, found := r.provider.Get()
	if !found {
		return nil, ErrProviderNotSet
	}

	return p, nil
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// AddAndQueueTorrent adds a torrent to the debrid service and queues it for automatic download
func (r *Repository) AddAndQueueTorrent(opts debrid.AddTorrentOptions, destination string, mId int) (string, error) {
	hTorrentItemId, err := triggerOnAddTorrentRequestedHook(&opts, &destination, &mId)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(destination) {
		return "", fmt.Errorf("debrid: Failed to add torrent, destination must be an absolute path")
	}

	provider, err := r.GetProvider()
	if err != nil {
		return "", err
	}

	torrentItemId := hTorrentItemId
	if torrentItemId == "" {
		// Add the torrent to the debrid service
		torrentItemId, err = provider.AddTorrent(opts)
		if err != nil {
			return "", err
		}
	}

	// Add the torrent item to the database (so it can be downloaded automatically once it's ready)
	// We ignore the error since it's non-critical
	_ = r.db.UpsertDebridTorrentItem(&models.DebridTorrentItem{
		TorrentItemID: torrentItemId,
		Destination:   destination,
		Provider:      provider.GetSettings().ID,
		MediaId:       mId,
	})

	event := &DebridAddTorrentEvent{
		Options:       opts,
		Destination:   destination,
		MediaID:       mId,
		TorrentItemID: torrentItemId,
	}

	_ = hook.GlobalHookManager.OnDebridAddTorrent().Trigger(event)

	return torrentItemId, nil
}

func triggerOnAddTorrentRequestedHook(opts *debrid.AddTorrentOptions, destination *string, mediaID *int) (string, error) {
	requestedEvent := &DebridAddTorrentRequestedEvent{
		Options:     *opts,
		Destination: *destination,
		MediaID:     *mediaID,
	}

	if err := hook.GlobalHookManager.OnDebridAddTorrentRequested().Trigger(requestedEvent); err != nil {
		return "", err
	}

	*opts = requestedEvent.Options
	*destination = requestedEvent.Destination
	*mediaID = requestedEvent.MediaID

	if requestedEvent.DefaultPrevented {
		if requestedEvent.TorrentItemID == "" {
			return "", fmt.Errorf("debrid: add torrent prevented by hook without torrent item id")
		}
		return requestedEvent.TorrentItemID, nil
	}

	return "", nil
}

// GetTorrentInfo retrieves information about a torrent.
// This is used for file section for debrid streaming.
// On Real Debrid, this adds the torrent to the user's account.
func (r *Repository) GetTorrentInfo(opts debrid.GetTorrentInfoOptions) (*debrid.TorrentInfo, error) {
	provider, err := r.GetProvider()
	if err != nil {
		return nil, err
	}

	torrentInfo, err := provider.GetTorrentInfo(opts)
	if err != nil {
		return nil, err
	}

	// Remove non-video files
	torrentInfo.Files = debrid.FilterVideoFiles(torrentInfo.Files)

	return torrentInfo, nil
}

func (r *Repository) HasProvider() bool {
	return r.provider.IsPresent()
}

func (r *Repository) GetSettings() *models.DebridSettings {
	return r.settings
}

func (r *Repository) IsDownloadActive(itemID string) bool {
	if r.ctxMap == nil {
		return false
	}

	return r.ctxMap.Has(itemID)
}

// CancelDownload cancels the download for the given item ID
func (r *Repository) CancelDownload(itemID string) error {
	cancelFunc, found := r.ctxMap.Get(itemID)
	if !found {
		return fmt.Errorf("no download found for item ID: %s", itemID)
	}

	// Call the cancel function to cancel the download
	if cancelFunc != nil {
		cancelFunc()
	}

	r.ctxMap.Delete(itemID)

	// Notify that the download has been cancelled
	r.wsEventManager.SendEvent(events.DebridDownloadProgress, map[string]interface{}{
		"status": "cancelled",
		"itemID": itemID,
	})

	return nil
}

// smFor returns the per-user StreamManager, creating it on first use. Each user's
// stream state is isolated so concurrent streams never clobber each other.
func (r *Repository) smFor(userID uint) *StreamManager {
	sm, _ := r.streamManagers.GetOrSet(userID, func() (*StreamManager, error) {
		m := NewStreamManager(r)
		// Restore the user's last active stream (if still fresh) so a re-issue after a
		// server restart reuses the cached link instantly instead of re-resolving.
		m.loadPersistedActiveStream(userID)
		return m, nil
	})
	return sm
}

func (r *Repository) StartStream(ctx context.Context, opts *StartStreamOptions) error {
	return r.smFor(opts.UserID).startStream(ctx, opts)
}

// PreloadStream resolves and caches the next episode's stream URL for instant playback.
func (r *Repository) PreloadStream(ctx context.Context, opts *StartStreamOptions) error {
	return r.smFor(opts.UserID).preloadStream(ctx, opts)
}

// GetStreamURL returns the stream URL of any currently-active stream. With per-user
// stream managers there may be several; this returns the first non-empty one (used by
// single-host features like Nakama/plugins).
func (r *Repository) GetStreamURL() (string, bool) {
	var url string
	r.streamManagers.Range(func(_ uint, sm *StreamManager) bool {
		if u := sm.getCurrentStreamUrl(); u != "" {
			url = u
			return false
		}
		return true
	})
	return url, url != ""
}

// UserStreamShare is what the watch-room "join stream" path needs to let a peer (re)play the
// host's stream. Preferred path: reuse the SELECTION (TorrentItemId + FileId) — already added
// to the debrid account — and have the peer resolve its OWN fresh CDN link from it (cheap, no
// createtorrent), so peers don't contend on one resolved link. StreamUrl is the host's link,
// kept as a fallback for cases with no torrent item (e.g. a direct-StreamUrl release).
type UserStreamShare struct {
	StreamUrl     string
	Filepath      string
	TorrentItemId string
	FileId        string
}

// GetUserStreamShare returns the shareable selection for a user's currently-active debrid
// stream. Returns ok=false when that user has no active stream in memory yet.
func (r *Repository) GetUserStreamShare(userID uint) (share UserStreamShare, ok bool) {
	sm, found := r.streamManagers.Get(userID)
	if !found {
		return UserStreamShare{}, false
	}
	// Consistent triple — never a torn selection (URL refreshed but file id stale, or vice-versa).
	streamUrl, torrentItemId, fileId := sm.shareSnapshot()
	if streamUrl == "" {
		return UserStreamShare{}, false
	}
	filepath := ""
	sm.preloadMu.Lock()
	if e, ok := sm.preloads[sm.lastConsumedKey]; ok && e != nil {
		filepath = e.filepath
	}
	sm.preloadMu.Unlock()
	return UserStreamShare{
		StreamUrl:     streamUrl,
		Filepath:      filepath,
		TorrentItemId: torrentItemId,
		FileId:        fileId,
	}, true
}

func (r *Repository) CancelStream(opts *CancelStreamOptions) {
	r.smFor(opts.UserID).cancelStream(opts)
}

func (r *Repository) setPreviousStreamOptions(opts *StartStreamOptions) {
	r.prevOptsMu.Lock()
	r.previousStreamOptions = mo.Some(opts)
	r.prevOptsMu.Unlock()
}

func (r *Repository) GetPreviousStreamOptions() (*StartStreamOptions, bool) {
	r.prevOptsMu.RLock()
	defer r.prevOptsMu.RUnlock()
	return r.previousStreamOptions.Get()
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
