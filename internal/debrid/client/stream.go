package debrid_client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"seanime/internal/api/anilist"
	"seanime/internal/database/db_bridge"
	"seanime/internal/database/models"
	"seanime/internal/debrid/debrid"
	"seanime/internal/directstream"
	"seanime/internal/events"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/hook"
	"seanime/internal/library/anime"
	"seanime/internal/library/playbackmanager"
	"seanime/internal/util"
	"strconv"
	"sync"
	"time"

	"github.com/samber/mo"
)

type (
	StreamManager struct {
		repository *Repository
		// stateMu guards the active-stream "share" scalars below (currentTorrentItemId,
		// currentStreamUrl, currentFileId, previousStreamOptions). startStream's download
		// goroutine writes them while the watch-room / Nakama / plugin host paths read them
		// from other goroutines (Repository.GetStreamURL / GetUserStreamShare) — without this
		// a peer could read a torn selection (URL refreshed but file id not, or vice-versa).
		// Always go through the get*/set* helpers below, never the fields directly.
		// The two ctx cancel funcs are under this lock too (swap/cancel helpers below): the old
		// unlocked pattern had every goroutine cancel-and-nil the FIELD, so a previous stream's
		// goroutine exiting late (its poll backoff is up to 4s) cancelled the NEXT stream's
		// context — a silent "second episode never starts". Goroutines now cancel their own
		// captured cancel func; the field is only for cancelling the CURRENT stream.
		stateMu               sync.RWMutex
		currentTorrentItemId  string
		downloadCtxCancelFunc context.CancelFunc

		currentStreamUrl string
		// currentFileId is the active stream's debrid file id. Captured so the watch-room
		// "join stream" path can share the SELECTION (torrent item + file) with peers, who
		// then resolve their OWN CDN link from it (no shared single link to contend on).
		currentFileId string

		playbackSubscriberCtxCancelFunc context.CancelFunc

		// Preloaded streams, resolved ahead of time so playback starts instantly. Keyed by
		// media+episode (preloadKey). Holds the ~80% next-episode preload plus the server-side
		// continue-watching prewarm (next-up of the last few shows watched).
		preloadMu       sync.Mutex
		preloads        map[string]*preloadedDebridStream // resolved, ready to consume
		preloadInflight map[string]context.CancelFunc     // in-flight resolves, for dedupe/cancel
		// preloadFailedAt is the negative cache for failed preload resolves (no torrents yet):
		// skip re-attempts for preloadFailureBackoff so a just-aired target doesn't re-search
		// every tick. ponytail: map[string]time.Time negative cache, no persistence.
		preloadFailedAt map[string]time.Time
		// lastConsumedKey is the preload entry currently being played. Kept (not deleted) on
		// consume so re-pressing/restarting the same episode is instant; deleted on episode end
		// (a different episode starts, or the stream is cancelled). TTL is the staleness backstop.
		lastConsumedKey string
		// previousStreamOptions is THIS user's last stream options, used by cancelStream to
		// resolve the right per-user directStream. (The repository keeps a separate
		// last-active copy for host/plugin accessors.)
		previousStreamOptions mo.Option[*StartStreamOptions]
	}

	// preloadedDebridStream holds a resolved debrid SELECTION (torrent + added torrentItemId)
	// plus its last-resolved stream URL for a future episode.
	preloadedDebridStream struct {
		opts          *StartStreamOptions
		streamUrl     string
		fileId        string
		filepath      string
		media         *anilist.BaseAnime
		torrent       *hibiketorrent.AnimeTorrent
		torrentItemId string
		resolvedAt    time.Time     // when the SELECTION was resolved (governs re-search via ttl)
		ttl           time.Duration // selection lifetime: 24h finished show, 3h currently releasing
		urlResolvedAt time.Time     // when streamUrl was last (re)resolved — debrid URLs expire sooner
		priority      bool          // protected from eviction (continue-watching set)
	}

	StreamPlaybackType string

	StreamStatus string

	StreamState struct {
		Status      StreamStatus `json:"status"`
		TorrentName string       `json:"torrentName"`
		Message     string       `json:"message"`
	}

	StartStreamOptions struct {
		MediaId       int
		EpisodeNumber int                         // RELATIVE Episode number to identify the file
		AniDBEpisode  string                      // Anizip episode
		Torrent       *hibiketorrent.AnimeTorrent // Selected torrent
		FileId        string                      // File ID or index
		FileIndex     *int                        // Index of the file to stream (Manual selection)
		// SharedTorrentItemId, when set (with AutoSelect=false), reuses an ALREADY-ADDED debrid
		// torrent item instead of adding one: the stream skips AddTorrent and resolves its own
		// fresh CDN link from this item id (cheap — no createtorrent). The watch-room join path
		// sets it from the host's active selection so peers reuse the selection without
		// contending on the host's single resolved link (the cause of a follower never loading).
		SharedTorrentItemId string
		// DirectCdnCapable is set by clients that can play a raw debrid CDN URL themselves
		// (Denshi injects CORS headers in its main process; a plain web tab cannot). Combined
		// with the DirectCdnPlayback setting + provider allowlist to decide direct mode.
		DirectCdnCapable bool
		UserAgent        string
		ClientId         string
		// UserID is the Seanime user who owns this stream; routes playback/events to
		// their per-session modules so users stream independently. 0 = admin/global.
		UserID            uint
		PlaybackType      StreamPlaybackType
		AutoSelect        bool
		BatchEpisodeFiles *hibiketorrent.BatchEpisodeFiles
		// Preload is true when the stream should only be resolved and cached (not played).
		Preload bool
		// PrewarmMetadata, when set on a preload, also pre-parses the MKV metadata (skips the
		// "Loading metadata" step on play). Gated to high-certainty targets (client next-episode
		// preloads), NOT the speculative continue-watching prewarm, since it downloads fonts.
		PrewarmMetadata bool
		// Priority marks a high-value preload (the server's continue-watching next-up set the user
		// is most likely to click). Such entries survive eviction over speculative hover prewarms.
		Priority bool
	}

	CancelStreamOptions struct {
		// Whether to remove the torrent from the debrid service
		RemoveTorrent bool `json:"removeTorrent"`
		// UserID selects which user's stream to cancel (per-user stream managers). 0 falls
		// back to the system/admin manager. The handler sets it from the request user.
		UserID uint `json:"-"`
	}
)

const (
	StreamStatusDownloading StreamStatus = "downloading"
	StreamStatusReady       StreamStatus = "ready"
	StreamStatusFailed      StreamStatus = "failed"
	StreamStatusStarted     StreamStatus = "started"
)

func NewStreamManager(repository *Repository) *StreamManager {
	return &StreamManager{
		repository:            repository,
		currentTorrentItemId:  "",
		preloads:              make(map[string]*preloadedDebridStream),
		preloadInflight:       make(map[string]context.CancelFunc),
		preloadFailedAt:       make(map[string]time.Time),
		previousStreamOptions: mo.None[*StartStreamOptions](),
	}
}

// --- active-stream share-state accessors (guarded by stateMu) ---

func (s *StreamManager) setCurrentStreamUrl(v string) {
	s.stateMu.Lock()
	s.currentStreamUrl = v
	s.stateMu.Unlock()
}

func (s *StreamManager) getCurrentStreamUrl() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.currentStreamUrl
}

func (s *StreamManager) setCurrentTorrentItemId(v string) {
	s.stateMu.Lock()
	s.currentTorrentItemId = v
	s.stateMu.Unlock()
}

func (s *StreamManager) getCurrentTorrentItemId() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.currentTorrentItemId
}

func (s *StreamManager) setCurrentFileId(v string) {
	s.stateMu.Lock()
	s.currentFileId = v
	s.stateMu.Unlock()
}

// shareSnapshot returns a consistent (url, torrentItemId, fileId) triple for the
// watch-room "join stream" path, so a peer never reads a half-updated selection.
func (s *StreamManager) shareSnapshot() (url, torrentItemId, fileId string) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.currentStreamUrl, s.currentTorrentItemId, s.currentFileId
}

func (s *StreamManager) setPreviousStreamOptions(opts *StartStreamOptions) {
	s.stateMu.Lock()
	s.previousStreamOptions = mo.Some(opts)
	s.stateMu.Unlock()
}

// setDownloadCancel installs the CURRENT stream's download cancel func (nil to clear).
func (s *StreamManager) setDownloadCancel(c context.CancelFunc) {
	s.stateMu.Lock()
	s.downloadCtxCancelFunc = c
	s.stateMu.Unlock()
}

// cancelDownloadCtx cancels the current stream's download context (if any) and clears the slot.
func (s *StreamManager) cancelDownloadCtx() {
	s.stateMu.Lock()
	c := s.downloadCtxCancelFunc
	s.downloadCtxCancelFunc = nil
	s.stateMu.Unlock()
	if c != nil {
		c()
	}
}

func (s *StreamManager) setPlaybackSubscriberCancel(c context.CancelFunc) {
	s.stateMu.Lock()
	s.playbackSubscriberCtxCancelFunc = c
	s.stateMu.Unlock()
}

func (s *StreamManager) cancelPlaybackSubscriberCtx() {
	s.stateMu.Lock()
	c := s.playbackSubscriberCtxCancelFunc
	s.playbackSubscriberCtxCancelFunc = nil
	s.stateMu.Unlock()
	if c != nil {
		c()
	}
}

func (s *StreamManager) getPreviousStreamOptions() (*StartStreamOptions, bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.previousStreamOptions.Get()
}

const (
	// Selection lifetime — how long a preloaded SELECTION (torrent + added torrentItemId) is
	// kept before re-searching/re-adding (which costs a TorBox createtorrent, limited to
	// 60/hour). A torrent doesn't die in a day; a currently-releasing show is re-checked
	// every few hours in case a better release appears (releases materially change only in
	// the first ~day; 1h caused hourly re-search churn + badge-flicker dead windows).
	preloadSelectionTTL          = 24 * time.Hour
	preloadSelectionTTLReleasing = 3 * time.Hour
	// urlRefreshTTL bounds how long a resolved debrid stream URL is trusted before we
	// re-resolve it (cheaply, from the already-added torrentItemId — no createtorrent) on
	// consume. An untouched TorBox CDN link stays valid ~3h, so 2h leaves a comfortable 1h
	// safety margin while avoiding a refresh on every play of a recently-prewarmed episode.
	urlRefreshTTL = 2 * time.Hour
	// maxSpeculativePreloads caps non-priority preloads. With the hover prewarm dropped these
	// are rare; the continue-watching (priority) set is uncapped (bounded at the source).
	maxSpeculativePreloads = 8
	// preloadFailureBackoff is the negative cache for failed preload resolves: a just-aired
	// episode with no torrents yet would otherwise re-run a full aggregator search every tick.
	// Real plays (startStream) are unaffected — this gates only background preloads.
	preloadFailureBackoff = 30 * time.Minute
	// preloadResolveTimeout hard-caps a single background preload resolve (search + debrid add +
	// requestdl). Without it, preloadCtx is derived from context.Background() and a stuck resolve
	// (e.g. a persistently-failing requestdl on a ready torrent) spins forever, leaking the
	// synchronous prewarm-drain goroutine and hammering the shared requestdl limiter against
	// real plays. Generous — a normal resolve completes in seconds.
	preloadResolveTimeout = 3 * time.Minute
	// batchFanOutCount caps how many episodes AFTER a preloaded one are fanned out from the
	// same batch torrent (URL-only, ~1 requestdl each; zero search/createtorrent).
	batchFanOutCount = 2
)

// selectionTTLForMedia returns the selection lifetime for a media: short (hourly re-check)
// for a currently-releasing show so a better release can be picked up, long (a day) otherwise.
func selectionTTLForMedia(media *anilist.BaseAnime) time.Duration {
	if media != nil && media.GetStatus() != nil && *media.GetStatus() == anilist.MediaStatusReleasing {
		return preloadSelectionTTLReleasing
	}
	return preloadSelectionTTL
}

// preloadKey identifies a preload slot by the episode it targets.
func preloadKey(opts *StartStreamOptions) string {
	// Selection intent (auto vs manual) is part of the key: a manual-pick preload must not
	// occupy the auto-select slot — it made the tick skip the real prewarm as "already fresh"
	// while the auto-select play then missed on the intent match (warm badge, cold play).
	return fmt.Sprintf("%d|%d|%s|%t", opts.MediaId, opts.EpisodeNumber, opts.AniDBEpisode, opts.AutoSelect)
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

const (
	PlaybackTypeNone           StreamPlaybackType = "none"
	PlaybackTypeNoneAndAwait   StreamPlaybackType = "noneAndAwait"
	PlaybackTypeDefault        StreamPlaybackType = "default"
	PlaybackTypeNativePlayer   StreamPlaybackType = "nativeplayer"
	PlaybackTypeExternalPlayer StreamPlaybackType = "externalPlayerLink"
)

// ds resolves the DirectStream manager for this stream's user (per-session), or the
// global (admin) one when no per-user resolver/user is set.
func (s *StreamManager) ds(opts *StartStreamOptions) *directstream.Manager {
	if opts != nil {
		return s.repository.dsFor(opts.UserID)
	}
	return s.repository.directStreamManager
}

// pb resolves the PlaybackManager for this stream's user (per-session), or the
// global (admin) one when no per-user resolver/user is set.
func (s *StreamManager) pb(opts *StartStreamOptions) *playbackmanager.PlaybackManager {
	if s.repository.sessionModulesFunc != nil && opts != nil {
		if _, pm := s.repository.sessionModulesFunc(opts.UserID); pm != nil {
			return pm
		}
	}
	return s.repository.playbackManager
}

// ev resolves the WS event manager for this stream's user (per-session overlay/loader
// events), or the repo's default (admin-scoped) manager when no resolver/user is set.
// This is what stops a non-admin's "Selecting/Adding torrent…" overlay from leaking to
// the admin (and ensures it actually reaches the streaming user).
func (s *StreamManager) ev(opts *StartStreamOptions) events.WSEventManagerInterface {
	if opts != nil {
		return s.repository.evFor(opts.UserID)
	}
	return s.repository.wsEventManager
}

// invalidatePrewarmBadges tells this user's clients to refetch the prewarm badge set. Fired
// whenever prewarm state actually CHANGES server-side (entry stored, dropped, cleaned up) —
// the old client-side invalidate-on-preload-POST raced the async resolve and always refetched
// before anything was warm, which is why badges only ever updated on page remounts.
func (s *StreamManager) invalidatePrewarmBadges(opts *StartStreamOptions) {
	if em := s.ev(opts); em != nil {
		em.SendEvent(events.InvalidateQueries, []string{events.DebridGetPrewarmStatusEndpoint})
	}
}

// startStream is called by the client to start streaming a torrent
// directCdnEligible reports whether this stream should hand the raw CDN link to the client
// (native player pulls video straight from the debrid CDN; the server keeps its own link for
// metadata/subtitle readers). Requires client capability, the DirectCdnPlayback setting, and
// an allowlisted provider — TorBox only (RD IP-locks links, a client fetch would 403).
func (s *StreamManager) directCdnEligible(opts *StartStreamOptions) bool {
	return directCdnEligibleWith(s.repository.GetSettings(), opts)
}

// directCdnEligibleWith is the pure eligibility check (unit-testable).
func directCdnEligibleWith(settings *models.DebridSettings, opts *StartStreamOptions) bool {
	if opts == nil || opts.PlaybackType != PlaybackTypeNativePlayer || !opts.DirectCdnCapable {
		return false
	}
	return settings != nil && settings.DirectCdnPlayback && settings.Provider == "torbox"
}

// resolveClientCdnUrl returns the CDN link the client plays from in direct mode. The original
// design resolved a SECOND link so client video and server readers wouldn't share one link's
// rate limit — but TorBox's requestdl is idempotent per (torrent, file): production logs show
// both resolves returning byte-identical URLs, so the extra call only burned the paced
// requestdl budget. Client and server inherently share the one link; the server side copes via
// the per-link connection gate, paced subtitle walk, and transient-429 retries.
// ponytail: if a future allowlisted provider mints distinct links per request, do a real
// second resolve here — the ClientStreamUrl plumbing downstream already supports it.
// knownFileSizeFor is knownFileSize with the manager's provider resolved for it. Returns 0 (skip
// the check) when there is no provider or it can't answer for free.
func (s *StreamManager) knownFileSizeFor(torrentItemId, fileId string) int64 {
	provider, err := s.repository.GetProvider()
	if err != nil {
		return 0
	}
	return knownFileSize(provider, torrentItemId, fileId)
}

// knownFileSize returns the size the provider reports for a file, or 0 when it can't answer for
// free. Providers opt in via debrid.FileSizeKnower; it must never cost an API call, so a miss just
// means the truncation check is skipped for this play (see httpBaseStream.expectedSize).
func knownFileSize(provider debrid.Provider, torrentItemId, fileId string) int64 {
	if provider == nil || torrentItemId == "" || fileId == "" {
		return 0
	}
	k, ok := provider.(debrid.FileSizeKnower)
	if !ok {
		return 0
	}
	size, ok := k.KnownFileSize(torrentItemId, fileId)
	if !ok {
		return 0
	}
	return size
}

func (s *StreamManager) resolveClientCdnUrl(torrentItemId, fileId, serverUrl string) string {
	return serverUrl
}

func (s *StreamManager) startStream(ctx context.Context, opts *StartStreamOptions) (err error) {
	defer util.HandlePanicInModuleWithError("debrid/client/StartStream", &err)

	// Reuse a preloaded stream if one matches this episode (native player + external player link).
	if canReusePreloadedStream(opts.PlaybackType) {
		key := preloadKey(opts)
		droppedPrev := false
		s.preloadMu.Lock()
		// A different episode is starting → the previously-consumed one has ended; drop its kept
		// entry (replays of the SAME episode keep theirs, so this leaves a same-key hit intact).
		if s.lastConsumedKey != "" && s.lastConsumedKey != key {
			// Release the finished episode's MKV metadata (font attachments in RAM) too.
			if prev, ok := s.preloads[s.lastConsumedKey]; ok {
				if dm := s.ds(opts); dm != nil {
					dm.DropStreamMetadata(prev.streamUrl)
				}
				droppedPrev = true
			}
			delete(s.preloads, s.lastConsumedKey)
			s.lastConsumedKey = ""
		}
		cached, ok := s.preloads[key]
		fresh := ok && time.Since(cached.resolvedAt) <= cached.ttl
		hit := fresh && debridStreamOptionsMatch(opts, cached.opts)
		if ok && !fresh {
			delete(s.preloads, key) // stale (expired) → drop. A fresh-but-intent-mismatch entry
			// (e.g. a manual pick when this episode was auto-select-preloaded) is kept, not consumed.
		}
		if hit {
			// Keep the entry so a restart/re-press of this episode is instant; it's removed on
			// episode end (next episode or cancelStream) and bounded by the TTL/slot cap.
			s.lastConsumedKey = key
		}
		s.preloadMu.Unlock()
		if droppedPrev {
			s.invalidatePrewarmBadges(opts)
		}
		if hit {
			if err := s.playPreloadedStream(ctx, opts, cached); !errors.Is(err, errPreloadedLinkDead) {
				return err
			}
			// Dead preloaded link → fall through to the cold resolve below (the open session,
			// if any, stays alive and the cold path re-enters it).
		} else if opts.AutoSelect {
			// In-memory miss — try the shared, account-partitioned DB before a cold resolve
			// (cross-user reuse + post-restart survival). Auto-select only (a manual pick is
			// never shared); the probe uses a fast-fail timeout so a dead link falls through
			// cheaply. hydrate re-resolves/drops safely on a dead or removed torrent item.
			if entry, ok := s.hydratePrewarmFromDB(ctx, opts, prewarmProbeTimeoutPlay); ok {
				s.preloadMu.Lock()
				s.lastConsumedKey = key
				s.preloadMu.Unlock()
				if err := s.playPreloadedStream(ctx, opts, entry); !errors.Is(err, errPreloadedLinkDead) {
					return err
				}
			}
		}
	}

	// Per-phase timings — the debrid path's equivalent of torrentstream's logDiagnostics.
	// Summarized in one Info line when the stream is ready.
	streamStartedAt := time.Now()
	var mediaInfoDur, selectionDur, addTorrentDur time.Duration

	s.setPreviousStreamOptions(opts)            // this user's last stream (for cancel)
	s.repository.setPreviousStreamOptions(opts) // last-active (host/plugin accessors)

	s.repository.logger.Info().
		Str("user", s.repository.usernameFor(opts.UserID)).
		Str("clientId", opts.ClientId).
		Any("playbackType", opts.PlaybackType).
		Int("mediaId", opts.MediaId).Msgf("debridstream: Starting stream for episode %s", opts.AniDBEpisode)

	// Cancel the previous stream's download/subscriber contexts if they're running
	s.cancelDownloadCtx()
	s.cancelPlaybackSubscriberCtx()

	provider, err := s.repository.GetProvider()
	if err != nil {
		return fmt.Errorf("debridstream: Failed to start stream: %w", err)
	}

	s.ev(opts).SendEvent(events.ShowIndefiniteLoader, "debridstream")
	//defer func() {
	//	s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
	//}()

	if opts.PlaybackType == PlaybackTypeNativePlayer {
		s.ds(opts).BeginOpen(opts.ClientId, "Selecting torrent...", func() {
			// Keep the torrent on teardown (was RemoveTorrent:true) so the shared prewarm cache can
			// reuse it. Cached torrents are free to keep (no active-slot cost) and the TTL sweeper
			// reclaims the DB rows; hydrate validates + falls back if TorBox evicts an item anyway.
			s.repository.CancelStream(&CancelStreamOptions{RemoveTorrent: false, UserID: opts.UserID})
		})
	}

	//
	// Get the media info
	//
	mediaInfoStart := time.Now()
	media, _, err := s.getMediaInfo(ctx, opts.MediaId)
	mediaInfoDur = time.Since(mediaInfoStart)
	if err != nil {
		s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
		return err
	}
	if opts.PlaybackType == PlaybackTypeNativePlayer && !s.ds(opts).IsOpenActive(opts.ClientId) {
		s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
		return nil
	}

	episodeNumber := opts.EpisodeNumber
	aniDbEpisode := strconv.Itoa(episodeNumber)

	selectedTorrent := opts.Torrent
	fileId := opts.FileId
	filepath := ""
	// directStreamUrl is non-empty for pre-resolved direct streams (StreamUrl on the result).
	// When set, we skip AddTorrent/GetTorrentStreamUrl below and play the URL directly.
	directStreamUrl := ""

	if opts.AutoSelect {

		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: "-",
			Message:     "Selecting best torrent...",
		})

		selectionStart := time.Now()
		pt, err := s.repository.findBestTorrent(ctx, provider, media, opts.EpisodeNumber, opts.UserID, false)
		selectionDur = time.Since(selectionStart)
		if err != nil {
			if opts.PlaybackType == PlaybackTypeNativePlayer {
				s.ds(opts).AbortOpen(opts.ClientId, err)
			}
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusFailed,
				TorrentName: "-",
				Message:     fmt.Sprintf("Failed to select best torrent, %v", err),
			})
			s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
			return fmt.Errorf("debridstream: Failed to start stream: %w", err)
		}
		selectedTorrent = pt.torrent
		fileId = pt.fileId
		filepath = pt.filepath
		directStreamUrl = pt.streamUrl
	} else {
		// Manual selection
		if selectedTorrent == nil {
			s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
			return fmt.Errorf("debridstream: Failed to start stream, no torrent provided")
		}

		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: selectedTorrent.Name,
			Message:     "Analyzing selected torrent...",
		})

		if selectedTorrent.StreamUrl != "" {
			// Pre-resolved direct stream — nothing to analyze.
			directStreamUrl = selectedTorrent.StreamUrl
			filepath = selectedTorrent.Name
		} else if opts.SharedTorrentItemId != "" {
			// Shared selection (watch-room peer): the torrent is already added under
			// SharedTorrentItemId and fileId is the host's file. Skip analysis; our own CDN
			// link is resolved from the item below. Name carries the host's filepath.
			filepath = selectedTorrent.Name
		} else if fileId == "" {
			// If no fileId is provided, we need to analyze the torrent to find the correct file
			var chosenFileIndex *int
			if opts.FileIndex != nil {
				chosenFileIndex = opts.FileIndex
			}
			selectionStart := time.Now()
			pt, err := s.repository.findBestTorrentFromManualSelection(provider, selectedTorrent, media, opts.EpisodeNumber, chosenFileIndex)
			selectionDur = time.Since(selectionStart)
			if err != nil {
				if opts.PlaybackType == PlaybackTypeNativePlayer {
					s.ds(opts).AbortOpen(opts.ClientId, err)
				}
				s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
					Status:      StreamStatusFailed,
					TorrentName: selectedTorrent.Name,
					Message:     fmt.Sprintf("Failed to analyze torrent, %v", err),
				})
				s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
				return fmt.Errorf("debridstream: Failed to analyze torrent: %w", err)
			}
			selectedTorrent = pt.torrent
			fileId = pt.fileId
			filepath = pt.filepath
		}
	}

	if selectedTorrent == nil {
		if opts.PlaybackType == PlaybackTypeNativePlayer {
			s.ds(opts).AbortOpen(opts.ClientId, fmt.Errorf("debridstream: Failed to start stream, no torrent provided"))
		}
		s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
		return fmt.Errorf("debridstream: Failed to start stream, no torrent provided")
	}
	if opts.PlaybackType == PlaybackTypeNativePlayer && !s.ds(opts).IsOpenActive(opts.ClientId) {
		s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
		return nil
	}

	// Pre-resolved direct streams have no torrent to add — torrentItemId stays empty and the
	// goroutine below uses directStreamUrl instead of polling GetTorrentStreamUrl.
	torrentItemId := ""
	if directStreamUrl == "" {
		if opts.SharedTorrentItemId != "" {
			// Reuse the host's already-added torrent item; our own CDN link is resolved from
			// it in the goroutine below. No AddTorrent (no createtorrent), and a fresh per-peer
			// link means peers don't contend on the host's single resolved link.
			torrentItemId = opts.SharedTorrentItemId
		} else {
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusDownloading,
				TorrentName: selectedTorrent.Name,
				Message:     "Adding torrent...",
			})

			// Add the torrent to the debrid service
			addTorrentStart := time.Now()
			torrentItemId, err = provider.AddTorrent(debrid.AddTorrentOptions{
				MagnetLink:   selectedTorrent.MagnetLink,
				InfoHash:     selectedTorrent.InfoHash,
				SelectFileId: fileId, // RD-only, download only the selected file
			})
			addTorrentDur = time.Since(addTorrentStart)
			if err != nil {
				s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
					Status:      StreamStatusFailed,
					TorrentName: selectedTorrent.Name,
					Message:     fmt.Sprintf("Failed to add torrent, %v", err),
				})
				s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
				return fmt.Errorf("debridstream: Failed to add torrent: %w", err)
			}

			// ponytail: no settle needed — GetTorrentStreamUrl's first poll is now 500ms out (with
			// backoff), which is plenty for the just-added item to become queryable.
		}
	}

	// Save the current selection (torrent item + file). Setting BOTH here — not the file id only at
	// resolve time (below) — makes the selection shareable to a watch-room peer as soon as it's known,
	// so the peer resolves its OWN CDN link CONCURRENTLY with ours instead of blocking on our fully-
	// resolved currentStreamUrl (which lands ~2-3s later at the bottom of the download goroutine). It
	// also closes the torn-selection window (new item id paired with a stale file id). fileId is final
	// from selection and does not change before the resolve.
	s.setCurrentTorrentItemId(torrentItemId)
	s.setCurrentFileId(fileId)
	ctx, cancelCtx := context.WithCancel(context.Background())
	s.setDownloadCancel(cancelCtx)

	readyCh := make(chan struct{})
	readyOnce := sync.Once{}
	ready := func() {
		readyOnce.Do(func() {
			close(readyCh)
		})
	}

	// Launch a goroutine that will listen to the added torrent's status
	go func(ctx context.Context) {
		defer util.HandlePanicInModuleThen("debrid/client/StartStream", func() {})
		defer func() {
			s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
		}()

		// Cancel OUR OWN context on exit — never the shared field: a previous stream's goroutine
		// exiting late (poll backoff up to 4s) used to cancel the NEXT stream's context through
		// the field, silently killing the new download ("second episode never starts").
		defer cancelCtx()

		s.repository.logger.Debug().Msg("debridstream: Listening to torrent status")

		var urlResolveDur, fileCheckDur time.Duration
		var streamUrl string
		if directStreamUrl != "" {
			// Pre-resolved direct stream — no download to await.
			streamUrl = directStreamUrl
		} else {
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusDownloading,
				TorrentName: selectedTorrent.Name,
				Message:     fmt.Sprintf("Downloading torrent..."),
			})

			itemCh := make(chan debrid.TorrentItem, 1)

			go func() {
				for item := range itemCh {
					if opts.PlaybackType == PlaybackTypeNativePlayer {
						// Same phrasing as the debrid-state event below — the loading screen merges
						// both channels by recency, so differing text ("Awaiting stream" vs
						// "Downloading torrent") ping-pongs per poll. The call is kept for its return
						// value, which is the open cancellation check.
						if !s.ds(opts).UpdateOpenStep(opts.ClientId, fmt.Sprintf("Downloading torrent: %d%%", item.CompletionPercentage)) {
							return
						}
					}

					s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
						Status:      StreamStatusDownloading,
						TorrentName: item.Name,
						Message:     fmt.Sprintf("Downloading torrent: %d%%", item.CompletionPercentage),
					})
				}
			}()

			// Await the stream URL
			// For Torbox, this will wait until the entire torrent is downloaded
			urlResolveStart := time.Now()
			url, err := provider.GetTorrentStreamUrl(ctx, debrid.StreamTorrentOptions{
				ID:     torrentItemId,
				FileId: fileId,
			}, itemCh)
			urlResolveDur = time.Since(urlResolveStart)

			go func() {
				close(itemCh)
			}()

			if ctx.Err() != nil {
				s.repository.logger.Debug().Msg("debridstream: Context cancelled, stopping stream")
				ready()
				return
			}
			if opts.PlaybackType == PlaybackTypeNativePlayer && !s.ds(opts).IsOpenActive(opts.ClientId) {
				ready()
				return
			}

			if err != nil {
				s.repository.logger.Err(err).Msg("debridstream: Failed to get stream URL")
				if !errors.Is(err, context.Canceled) {
					s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
						Status:      StreamStatusFailed,
						TorrentName: selectedTorrent.Name,
						Message:     fmt.Sprintf("Failed to get stream URL, %v", err),
					})
				}
				ready()
				return
			}

			streamUrl = url
		}

		skipCheckEvent := &DebridSkipStreamCheckEvent{
			StreamURL:  streamUrl,
			Retries:    4,
			RetryDelay: 8,
		}
		_ = hook.GlobalHookManager.OnDebridSkipStreamCheck().Trigger(skipCheckEvent)
		streamUrl = skipCheckEvent.StreamURL

		// Default prevented, we check if we can stream the file
		if skipCheckEvent.DefaultPrevented {
			fileCheckStart := time.Now()
			s.repository.logger.Debug().Msg("debridstream: Stream URL received, checking stream file")
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusDownloading,
				TorrentName: selectedTorrent.Name,
				Message:     "Checking stream file...",
			})

			retries := 0

		streamUrlCheckLoop:
			for { // Retry loop for a total of 4 times (32 seconds)
				select {
				case <-ctx.Done():
					s.repository.logger.Debug().Msg("debridstream: Context cancelled, stopping stream")
					return
				default:
					// Check if we can stream the URL
					if canStream, reason := CanStream(streamUrl); !canStream {
						if retries >= skipCheckEvent.Retries {
							s.repository.logger.Error().Msg("debridstream: Cannot stream the file")

							s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
								Status:      StreamStatusFailed,
								TorrentName: selectedTorrent.Name,
								Message:     fmt.Sprintf("Cannot stream this file: %s", reason),
							})
							return
						}
						s.repository.logger.Warn().Msg("debridstream: Rechecking stream file in 8 seconds")
						s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
							Status:      StreamStatusDownloading,
							TorrentName: selectedTorrent.Name,
							Message:     "Checking stream file...",
						})
						retries++
						time.Sleep(time.Duration(skipCheckEvent.RetryDelay) * time.Second)
						continue
					}
					break streamUrlCheckLoop
				}
			}
			fileCheckDur = time.Since(fileCheckStart)
		}

		s.repository.logger.Info().
			Dur("mediaInfo", mediaInfoDur).
			Dur("selection", selectionDur).
			Dur("addTorrent", addTorrentDur).
			Dur("urlResolve", urlResolveDur).
			Dur("fileCheck", fileCheckDur).
			Dur("total", time.Since(streamStartedAt)).
			Msg("debridstream: Stream is ready")

		// A cold resolve succeeded — clear the preload negative cache for this episode so the
		// background prewarm doesn't keep skipping a target that provably resolves now.
		s.preloadMu.Lock()
		delete(s.preloadFailedAt, preloadKey(opts))
		s.preloadMu.Unlock()

		// Signal to the client that the torrent is ready to stream
		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusReady,
			TorrentName: selectedTorrent.Name,
			Message:     "Ready to stream the file",
		})

		if ctx.Err() != nil {
			s.repository.logger.Debug().Msg("debridstream: Context cancelled, stopping stream")
			ready()
			return
		}

		windowTitle := media.GetPreferredTitle()
		if !media.IsMovieOrSingleEpisode() {
			windowTitle += fmt.Sprintf(" - Episode %s", aniDbEpisode)
		}

		event := &DebridSendStreamToMediaPlayerEvent{
			WindowTitle:  windowTitle,
			StreamURL:    streamUrl,
			Media:        media.ToBaseAnime(),
			AniDbEpisode: aniDbEpisode,
			PlaybackType: string(opts.PlaybackType),
		}
		err = hook.GlobalHookManager.OnDebridSendStreamToMediaPlayer().Trigger(event)
		if err != nil {
			s.repository.logger.Err(err).Msg("debridstream: Failed to send stream to media player")
		}
		windowTitle = event.WindowTitle
		streamUrl = event.StreamURL
		media := event.Media
		aniDbEpisode := event.AniDbEpisode
		playbackType := StreamPlaybackType(event.PlaybackType)

		if event.DefaultPrevented {
			s.repository.logger.Debug().Msg("debridstream: Stream prevented by hook")
			ready()
			return
		}

		s.setCurrentStreamUrl(streamUrl)
		// currentFileId (the shareable selection for watch-room peers) is already set early, at
		// selection time above — no need to re-set it here.
		// Snapshot the freshly-resolved stream so a server restart can replay it instantly.
		s.persistActiveStream(opts, streamUrl, torrentItemId, fileId, filepath, media, selectedTorrent, time.Now())

		switch playbackType {
		case PlaybackTypeNone:
			// No playback type selected, just signal to the client that the stream is ready
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "External player link sent",
			})
		case PlaybackTypeNoneAndAwait:
			// No playback type selected, just signal to the client that the stream is ready
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "External player link sent",
			})
			ready()

		case PlaybackTypeDefault:
			//
			// Start the stream
			//
			s.repository.logger.Debug().Msg("debridstream: Starting the media player")

			s.ev(opts).SendEvent(events.InfoToast, "Sending stream to media player...")
			s.ev(opts).SendEvent(events.ShowIndefiniteLoader, "debridstream")

			playbackSubscriberCtx, playbackSubscriberCancel := context.WithCancel(context.Background())
			s.setPlaybackSubscriberCancel(playbackSubscriberCancel)
			playbackSubscriber := s.pb(opts).SubscribeToPlaybackStatus("debridstream")

			// Sends the stream to the media player
			// DEVNOTE: Events are handled by the torrentstream.Repository module
			err = s.pb(opts).StartStreamingUsingMediaPlayer(windowTitle, &playbackmanager.StartPlayingOptions{
				Payload:   streamUrl,
				UserAgent: opts.UserAgent,
				ClientId:  opts.ClientId,
			}, media, aniDbEpisode)
			if err != nil {
				go s.pb(opts).UnsubscribeFromPlaybackStatus("debridstream")
				playbackSubscriberCancel()
				// Failed to start the stream, we'll drop the torrents and stop the server
				s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
					Status:      StreamStatusFailed,
					TorrentName: selectedTorrent.Name,
					Message:     fmt.Sprintf("Failed to send the stream to the media player, %v", err),
				})
				return
			}

			// Listen to the playback status
			// Reset the current stream url when playback is stopped
			go func() {
				defer util.HandlePanicInModuleThen("debridstream/PlaybackSubscriber", func() {})
				// Cancel our OWN context on exit, never the shared field (see download goroutine).
				defer playbackSubscriberCancel()
				select {
				case <-playbackSubscriberCtx.Done():
					s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
					s.pb(opts).UnsubscribeFromPlaybackStatus("debridstream")
					s.setCurrentStreamUrl("")
				case event := <-playbackSubscriber.EventCh:
					switch event.(type) {
					case playbackmanager.StreamStartedEvent:
						s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
					case playbackmanager.StreamStoppedEvent:
						go s.pb(opts).UnsubscribeFromPlaybackStatus("debridstream")
						s.setCurrentStreamUrl("")
					}
				}
			}()

		case PlaybackTypeExternalPlayer:
			// Send the external player link
			s.ev(opts).SendEventTo(opts.ClientId, events.ExternalPlayerOpenURL, struct {
				Url           string `json:"url"`
				MediaId       int    `json:"mediaId"`
				EpisodeNumber int    `json:"episodeNumber"`
				MediaTitle    string `json:"mediaTitle"`
			}{
				Url:           streamUrl,
				MediaId:       opts.MediaId,
				EpisodeNumber: opts.EpisodeNumber,
				MediaTitle:    media.GetPreferredTitle(),
			})

			// Signal to the client that the torrent has started playing (remove loading status)
			// We can't know for sure
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "External player link sent",
			})
		case PlaybackTypeNativePlayer:
			s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "",
			})

			if !s.ds(opts).IsOpenActive(opts.ClientId) {
				return
			}
			clientStreamUrl := ""
			if s.directCdnEligible(opts) {
				clientStreamUrl = s.resolveClientCdnUrl(torrentItemId, fileId, streamUrl)
			}
			err := s.ds(opts).PlayDebridStream(ctx, filepath, directstream.PlayDebridStreamOptions{
				StreamUrl:       streamUrl,
				ClientStreamUrl: clientStreamUrl,
				MediaId:         media.ID,
				AnidbEpisode:    opts.AniDBEpisode,
				Media:           media,
				Torrent:         selectedTorrent,
				FileId:          fileId,
				ExpectedSize:    knownFileSize(provider, torrentItemId, fileId),
				UserAgent:       opts.UserAgent,
				ClientId:        opts.ClientId,
				AutoSelect:      false,
			})
			if err != nil {
				s.repository.logger.Error().Err(err).Msg("directstream: Failed to prepare new stream")
				return
			}
		}

		go func() {
			defer util.HandlePanicInModuleThen("debridstream/AddBatchHistory", func() {})

			if selectedTorrent.IsBatch {
				_ = db_bridge.InsertTorrentstreamHistory(s.repository.db, media.GetID(), selectedTorrent, opts.BatchEpisodeFiles)

				s.ev(opts).SendEvent(events.InvalidateQueries, []string{events.GetTorrentstreamBatchHistoryEndpoint})
			}
		}()

		go s.chainNextEpisodePreload(opts, media)
	}(ctx)

	s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
		Status:      StreamStatusStarted,
		TorrentName: selectedTorrent.Name,
		Message:     "Stream started",
	})
	s.repository.logger.Info().Msg("debridstream: Stream started")

	if opts.PlaybackType == PlaybackTypeNoneAndAwait {
		s.repository.logger.Debug().Msg("debridstream: Waiting for stream to be ready")
		<-readyCh
		s.ev(opts).SendEvent(events.HideIndefiniteLoader, "debridstream")
	}

	return nil
}

func (s *StreamManager) cancelStream(opts *CancelStreamOptions) {
	// The playing episode is ending → drop only ITS kept preload entry (the one held for instant
	// replay). Other shows' continue-watching prewarms stay warm; stale entries self-evict via TTL.
	// Full reset (provider change/shutdown) uses ClearAllPreloads.
	var endedStreamUrl string
	droppedEntry := false
	s.preloadMu.Lock()
	if s.lastConsumedKey != "" {
		if prev, ok := s.preloads[s.lastConsumedKey]; ok {
			endedStreamUrl = prev.streamUrl
			droppedEntry = true
		}
		delete(s.preloads, s.lastConsumedKey)
		s.lastConsumedKey = ""
	}
	s.preloadMu.Unlock()
	if droppedEntry {
		if em := s.repository.evFor(opts.UserID); em != nil {
			em.SendEvent(events.InvalidateQueries, []string{events.DebridGetPrewarmStatusEndpoint})
		}
	}

	// Resolve the directStream of the user who owns the stream being cancelled — THIS
	// manager's own last stream (per-user), not the repository's last-active copy. When there
	// are no previous options (fresh per-user manager, e.g. right after a restart where
	// loadPersistedActiveStream restored preloads but not previousStreamOptions), fall back to
	// the cancel's own UserID rather than nil — otherwise ds(nil)/ev(nil) resolve the GLOBAL
	// (admin) manager and this cancel would abort the admin's open and hide their loader.
	var prevOpts *StartStreamOptions
	if p, ok := s.getPreviousStreamOptions(); ok {
		prevOpts = p
	} else if opts != nil {
		prevOpts = &StartStreamOptions{UserID: opts.UserID}
	}
	if dm := s.ds(prevOpts); dm != nil {
		dm.CloseOpen("")
		// Release the finished episode's cached MKV metadata (font attachments in RAM).
		// Both URLs: the preload entry's (binge path) AND the live stream's — a COLD-started
		// episode has no preload entry, so without the second call its parser (fonts, tens of
		// MB) lingered in RAM for the full 2h cache TTL.
		dm.DropStreamMetadata(endedStreamUrl)
		dm.DropStreamMetadata(s.getCurrentStreamUrl())
	}

	s.cancelDownloadCtx()

	s.ev(prevOpts).SendEvent(events.HideIndefiniteLoader, "debridstream")

	s.setCurrentStreamUrl("")

	torrentItemId := s.getCurrentTorrentItemId()
	if opts.RemoveTorrent && torrentItemId != "" {
		// Remove the torrent from the debrid service
		provider, err := s.repository.GetProvider()
		if err != nil {
			s.repository.logger.Err(err).Msg("debridstream: Failed to remove torrent")
			return
		}

		// Remove the torrent from the debrid service
		err = provider.DeleteTorrent(torrentItemId)
		if err != nil {
			s.repository.logger.Err(err).Msg("debridstream: Failed to remove torrent")
		}
	}

	// Clear the whole share triple together — leaving a stale itemId/fileId with an empty URL
	// hands a torn selection to anything reading shareSnapshot after a cancel.
	s.setCurrentTorrentItemId("")
	s.setCurrentFileId("")
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Preload
//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// debridStreamOptionsMatch reports whether two option sets target the same episode.
func debridStreamOptionsMatch(a, b *StartStreamOptions) bool {
	if a == nil || b == nil {
		return false
	}
	if a.MediaId != b.MediaId || a.EpisodeNumber != b.EpisodeNumber || a.AniDBEpisode != b.AniDBEpisode {
		return false
	}
	// A preload must match the SELECTION INTENT, not just the episode — otherwise a manual torrent
	// pick for an episode that was auto-select-preloaded would silently play the cached auto-selected
	// stream instead of the chosen one.
	if a.AutoSelect != b.AutoSelect {
		return false
	}
	if !a.AutoSelect {
		// Manual selection: require the same torrent + file.
		if a.FileId != b.FileId || !sameAnimeTorrent(a.Torrent, b.Torrent) {
			return false
		}
		if (a.FileIndex == nil) != (b.FileIndex == nil) {
			return false
		}
		if a.FileIndex != nil && b.FileIndex != nil && *a.FileIndex != *b.FileIndex {
			return false
		}
	}
	return true
}

// sameAnimeTorrent reports whether two torrents refer to the same release.
func sameAnimeTorrent(a, b *hibiketorrent.AnimeTorrent) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.InfoHash != "" && b.InfoHash != "" {
		return a.InfoHash == b.InfoHash
	}
	return a.Identity() == b.Identity()
}

// clearAllPreloadsLocked cancels every in-flight resolve and drops all cached streams.
// Used on provider change / shutdown — NOT on a normal stream cancel. Caller holds preloadMu.
func (s *StreamManager) clearAllPreloadsLocked() {
	for k, cancel := range s.preloadInflight {
		if cancel != nil {
			cancel()
		}
		delete(s.preloadInflight, k)
	}
	for k := range s.preloads {
		delete(s.preloads, k)
	}
	for k := range s.preloadFailedAt {
		delete(s.preloadFailedAt, k)
	}
	s.lastConsumedKey = ""
}

// evictIfNeededLocked enforces the speculative-preload budget. Continue-watching (priority) entries
// are NOT capped here — they're bounded at the source (3 shows + next-ep) and self-expire via TTL —
// so the browse/search/discover hover firehose can never evict them. Caller holds preloadMu.
func (s *StreamManager) evictIfNeededLocked(priority bool) {
	// Drop any TTL-expired entries first (either class) so the map can't grow unbounded over a
	// binge. Never the consumed entry: it's the stream being PLAYED (its URL is refreshed on
	// consume, so selection-TTL expiry mid-playback is meaningless) and share/cancel paths
	// still read it.
	for k, v := range s.preloads {
		if k == s.lastConsumedKey {
			continue
		}
		if time.Since(v.resolvedAt) > v.ttl {
			delete(s.preloads, k)
		}
	}
	if priority {
		return // continue-watching entries are not subject to the speculative budget
	}
	// Evict the oldest speculative entry while the speculative class is over budget.
	for {
		count := 0
		var oldestKey string
		var oldest time.Time
		for k, v := range s.preloads {
			if v.priority || k == s.lastConsumedKey {
				continue // never count or evict priority entries or the one being played
			}
			count++
			if oldestKey == "" || v.resolvedAt.Before(oldest) {
				oldestKey, oldest = k, v.resolvedAt
			}
		}
		if count < maxSpeculativePreloads || oldestKey == "" {
			break
		}
		delete(s.preloads, oldestKey)
	}
}

// preloadStream resolves the next episode's debrid stream URL ahead of time and caches it.
// It runs silently (no overlay events) so it doesn't disturb the episode still playing.
// The resolve itself runs on a goroutine (client-triggered preloads must not block playback).
func (s *StreamManager) preloadStream(ctx context.Context, opts *StartStreamOptions) error {
	return s.preloadStreamWith(ctx, opts, true)
}

// preloadStreamBlocking is preloadStream but the resolve runs SYNCHRONOUSLY. Used by the scheduled
// prewarm drain, which exists to serialize TorBox/aggregator pressure — the async variant made the
// "queue" a mere 1.5s kickoff stagger with all resolves still overlapping.
func (s *StreamManager) preloadStreamBlocking(ctx context.Context, opts *StartStreamOptions) error {
	return s.preloadStreamWith(ctx, opts, false)
}

func (s *StreamManager) preloadStreamWith(ctx context.Context, opts *StartStreamOptions, async bool) (err error) {
	defer util.HandlePanicInModuleWithError("debrid/client/preloadStream", &err)

	if s.repository.settings == nil || !s.repository.settings.PreloadNextStream {
		return nil
	}

	provider, err := s.repository.GetProvider()
	if err != nil {
		return fmt.Errorf("debridstream: Failed to preload stream: %w", err)
	}

	key := preloadKey(opts)

	s.preloadMu.Lock()
	// Skip if already resolved-fresh, or a resolve is already in flight for this episode.
	if existing, ok := s.preloads[key]; ok && time.Since(existing.resolvedAt) <= existing.ttl {
		// Upgrade a speculative entry to priority if a continue-watching prewarm arrives for it,
		// so an earlier hover/entry-mount preload of the same episode can't leave it evictable.
		if opts.Priority && !existing.priority {
			existing.priority = true
		}
		// An incoming metadata-prewarm request must still warm metadata even though the URL is
		// already resolved. Re-issued EVERY time (not just on a false→true upgrade): the parser
		// cache expires (2h) and sheds under CDN budget, so warmth must be re-established each
		// tick or the tier-1 target quietly goes cold. Free on a parser-cache hit.
		warmUrl := ""
		if opts.PrewarmMetadata && existing.streamUrl != "" {
			if existing.opts != nil {
				existing.opts.PrewarmMetadata = true
			}
			warmUrl = existing.streamUrl
		}
		s.preloadMu.Unlock()
		if warmUrl != "" {
			if dm := s.ds(opts); dm != nil {
				go dm.PrewarmStreamMetadata(warmUrl)
			}
		}
		return nil
	}
	if _, inflight := s.preloadInflight[key]; inflight {
		s.preloadMu.Unlock()
		return nil
	}
	// Negative cache: a recently-failed resolve (typically a just-aired episode with no torrents
	// yet) is not retried for preloadFailureBackoff — the tick otherwise re-runs a full aggregator
	// search every 10 minutes until releases appear.
	if failedAt, ok := s.preloadFailedAt[key]; ok {
		if time.Since(failedAt) < preloadFailureBackoff {
			s.preloadMu.Unlock()
			return nil
		}
		delete(s.preloadFailedAt, key)
	}
	// Bounded: a stuck resolve must not hang the drain goroutine or the requestdl limiter forever.
	preloadCtx, cancel := context.WithTimeout(context.Background(), preloadResolveTimeout)
	s.preloadInflight[key] = cancel
	s.preloadMu.Unlock()

	s.repository.logger.Info().
		Int("mediaId", opts.MediaId).
		Int("episodeNumber", opts.EpisodeNumber).
		Msg("debridstream: Preloading stream")

	run := func() {
		defer util.HandlePanicInModuleThen("debrid/client/preloadStream", func() {})
		defer func() {
			s.preloadMu.Lock()
			delete(s.preloadInflight, key)
			s.preloadMu.Unlock()
		}()

		// Shared-DB front-gate: reuse an already-resolved prewarm for this account+profile instead of
		// re-searching/re-adding (cross-user reuse + post-restart survival). Runs in the background so
		// the link-validation probe adds no user-facing latency; on a miss/dead-item it falls through.
		if entry, ok := s.hydratePrewarmFromDB(preloadCtx, opts, prewarmProbeTimeout); ok {
			// The hydrate satisfies the URL but never parses metadata — warm it here so a tier-1
			// target isn't "hot"-badged with a cold parser after a restart. Idempotent.
			if opts.PrewarmMetadata && entry.streamUrl != "" && preloadCtx.Err() == nil {
				if dm := s.ds(opts); dm != nil {
					dm.PrewarmStreamMetadata(entry.streamUrl)
				}
			}
			return
		}

		media, _, err := s.getMediaInfo(preloadCtx, opts.MediaId)
		if err != nil || preloadCtx.Err() != nil {
			return
		}

		selectedTorrent := opts.Torrent
		fileId := opts.FileId
		filepath := ""
		directStreamUrl := ""
		var otherEpisodeFiles map[int]*debrid.TorrentItemFile

		if opts.AutoSelect {
			// silent=true: background preload must not flash the playback pill.
			pt, err := s.repository.findBestTorrent(context.Background(), provider, media, opts.EpisodeNumber, opts.UserID, true)
			if err != nil {
				s.repository.logger.Warn().Err(err).Msg("debridstream: Preload failed to select torrent")
				// Negative-cache the miss (no releases yet) so background preloads don't re-search
				// every tick. Real plays still resolve cold and a success clears the entry.
				s.preloadMu.Lock()
				s.preloadFailedAt[key] = time.Now()
				s.preloadMu.Unlock()
				return
			}
			selectedTorrent, fileId, filepath, directStreamUrl = pt.torrent, pt.fileId, pt.filepath, pt.streamUrl
			otherEpisodeFiles = pt.otherEpisodeFiles
		} else {
			if selectedTorrent == nil {
				return
			}
			if selectedTorrent.StreamUrl != "" {
				directStreamUrl = selectedTorrent.StreamUrl
				filepath = selectedTorrent.Name
			} else if fileId == "" {
				pt, err := s.repository.findBestTorrentFromManualSelection(provider, selectedTorrent, media, opts.EpisodeNumber, opts.FileIndex)
				if err != nil {
					s.repository.logger.Warn().Err(err).Msg("debridstream: Preload failed to analyze torrent")
					return
				}
				selectedTorrent, fileId, filepath = pt.torrent, pt.fileId, pt.filepath
			}
		}

		if selectedTorrent == nil || preloadCtx.Err() != nil {
			return
		}

		// Pre-resolved direct stream — nothing to add or poll, cache the URL as-is.
		torrentItemId := ""
		streamUrl := directStreamUrl
		if directStreamUrl == "" {
			var err error
			torrentItemId, err = provider.AddTorrent(debrid.AddTorrentOptions{
				MagnetLink:   selectedTorrent.MagnetLink,
				InfoHash:     selectedTorrent.InfoHash,
				SelectFileId: fileId,
			})
			if err != nil {
				s.repository.logger.Warn().Err(err).Msg("debridstream: Preload failed to add torrent")
				return
			}

			// Drain progress updates silently while the debrid service caches the torrent.
			itemCh := make(chan debrid.TorrentItem, 1)
			go func() {
				for range itemCh { //nolint:revive
				}
			}()
			streamUrl, err = provider.GetTorrentStreamUrl(preloadCtx, debrid.StreamTorrentOptions{
				ID:     torrentItemId,
				FileId: fileId,
			}, itemCh)
			close(itemCh)

			if preloadCtx.Err() != nil {
				return
			}
			if err != nil || streamUrl == "" {
				s.repository.logger.Warn().Err(err).Msg("debridstream: Preload failed to resolve stream URL")
				return
			}
		}

		var stored *preloadedDebridStream
		s.preloadMu.Lock()
		// Only store if this preload wasn't superseded or cancelled in the meantime.
		if preloadCtx.Err() == nil {
			s.evictIfNeededLocked(opts.Priority)
			delete(s.preloadFailedAt, key) // resolve succeeded — clear the negative cache
			now := time.Now()
			stored = &preloadedDebridStream{
				opts:          opts,
				streamUrl:     streamUrl,
				fileId:        fileId,
				filepath:      filepath,
				media:         media.ToBaseAnime(),
				torrent:       selectedTorrent,
				torrentItemId: torrentItemId,
				resolvedAt:    now,
				ttl:           selectionTTLForMedia(media.ToBaseAnime()),
				urlResolvedAt: now,
				priority:      opts.Priority,
			}
			s.preloads[key] = stored
			s.repository.logger.Info().Str("torrent", selectedTorrent.Name).Msg("debridstream: Preloaded stream ready")
		}
		s.preloadMu.Unlock()
		// Share to the account-wide DB (best-effort) so other users on the same key / a restart
		// reuse it, and tell this user's clients the badge set changed.
		if stored != nil {
			s.persistPrewarm(opts, stored)
			s.invalidatePrewarmBadges(opts)
		}

		// Pre-parse MKV metadata for high-certainty targets (next-episode preloads) so the
		// play-time "Loading metadata" step is near-instant. Zero disk; gated by PrewarmMetadata
		// so the speculative continue-watching prewarm doesn't download fonts it may never use.
		if opts.PrewarmMetadata && streamUrl != "" && preloadCtx.Err() == nil && s.ds(opts) != nil {
			s.ds(opts).PrewarmStreamMetadata(streamUrl)
		}

		// Batch fan-out: sibling episodes inside the same added batch torrent cost ~1 requestdl
		// each (no search, no createtorrent) — warm the next couple so a binge stays ahead of
		// the player. Runs inline so the prewarm drain's serialization still bounds TorBox pressure.
		if stored != nil && torrentItemId != "" && len(otherEpisodeFiles) > 0 {
			s.fanOutBatchPreloads(preloadCtx, opts, media, selectedTorrent, torrentItemId, otherEpisodeFiles)
		}
	}

	if async {
		go run()
	} else {
		run()
	}

	return nil
}

// canReusePreloadedStream reports whether a playback type can consume a preloaded stream.
// Native player and external player link both just need a ready stream URL; the desktop
// media player ("default") path is interactive and not preloaded.
func canReusePreloadedStream(pt StreamPlaybackType) bool {
	return pt == PlaybackTypeNativePlayer || pt == PlaybackTypeExternalPlayer
}

// playPreloadedStream hands an already-resolved debrid stream to the right player without
// re-running the ~20s resolve. Supports the native player and external player link
// (the latter is what the mobile/mpv client uses).
// persistedActiveStream is the JSON-serializable snapshot of an active debrid stream's
// resolution, saved to the DB so it survives a server restart and can be replayed instantly.
type persistedActiveStream struct {
	Opts          *StartStreamOptions         `json:"opts"`
	StreamUrl     string                      `json:"streamUrl"`
	FileId        string                      `json:"fileId"`
	Filepath      string                      `json:"filepath"`
	Media         *anilist.BaseAnime          `json:"media"`
	Torrent       *hibiketorrent.AnimeTorrent `json:"torrent"`
	TorrentItemId string                      `json:"torrentItemId"`
	ResolvedAt    time.Time                   `json:"resolvedAt"`
	UrlResolvedAt time.Time                   `json:"urlResolvedAt"`
	TtlNanos      int64                       `json:"ttlNanos"`
}

// persistActiveStream snapshots the just-started stream to the DB so it can be replayed
// instantly after a server restart (no auto-select search — seamless reconnect). The
// resolved CDN link is reused directly while fresh, or cheaply re-resolved from the
// already-added torrentItemId (no createtorrent) if it has aged out. Best-effort.
func (s *StreamManager) persistActiveStream(opts *StartStreamOptions, streamUrl, torrentItemId, fileId, filepath string, media *anilist.BaseAnime, torrent *hibiketorrent.AnimeTorrent, urlResolvedAt time.Time) {
	defer util.HandlePanicInModuleThen("debrid/client/persistActiveStream", func() {})
	if s.repository.db == nil || opts == nil || streamUrl == "" {
		return
	}
	ttl := preloadSelectionTTL
	if media != nil {
		ttl = selectionTTLForMedia(media)
	}
	rec := persistedActiveStream{
		Opts: opts, StreamUrl: streamUrl, FileId: fileId, Filepath: filepath,
		Media: media, Torrent: torrent, TorrentItemId: torrentItemId,
		ResolvedAt: time.Now(), UrlResolvedAt: urlResolvedAt, TtlNanos: int64(ttl),
	}
	data, err := json.Marshal(&rec)
	if err != nil {
		return
	}
	_ = s.repository.db.UpsertDebridActiveStream(&models.DebridActiveStream{UserID: opts.UserID, Data: string(data)})
}

// loadPersistedActiveStream restores a user's last active stream (if still within its
// selection TTL) into the preload cache, so the next start of that episode — e.g. the client
// re-issuing after a server restart — reuses the cached link instantly instead of re-running
// auto-select. Called once when the user's StreamManager is created.
func (s *StreamManager) loadPersistedActiveStream(userID uint) {
	defer util.HandlePanicInModuleThen("debrid/client/loadPersistedActiveStream", func() {})
	if s.repository.db == nil {
		return
	}
	rec, ok := s.repository.db.GetDebridActiveStream(userID)
	if !ok || rec.Data == "" {
		return
	}
	var p persistedActiveStream
	if err := json.Unmarshal([]byte(rec.Data), &p); err != nil || p.Opts == nil {
		return
	}
	ttl := time.Duration(p.TtlNanos)
	if ttl <= 0 || time.Since(p.ResolvedAt) > ttl {
		s.repository.db.DeleteDebridActiveStream(userID) // selection expired — drop it
		return
	}
	entry := &preloadedDebridStream{
		opts: p.Opts, streamUrl: p.StreamUrl, fileId: p.FileId, filepath: p.Filepath,
		media: p.Media, torrent: p.Torrent, torrentItemId: p.TorrentItemId,
		resolvedAt: p.ResolvedAt, urlResolvedAt: p.UrlResolvedAt, ttl: ttl, priority: true,
	}
	key := preloadKey(p.Opts)
	s.preloadMu.Lock()
	s.preloads[key] = entry
	s.lastConsumedKey = key
	s.preloadMu.Unlock()
	s.repository.logger.Debug().Int("mediaId", p.Opts.MediaId).Msg("debridstream: Restored persisted active stream for instant reconnect")
}

// errPreloadedLinkDead signals that a preloaded stream's CDN link is dead and could not be
// refreshed. startStream treats it as "fall back to a cold resolve" — the user's click still
// plays, just without the head start — instead of surfacing an error and burning the click.
var errPreloadedLinkDead = errors.New("debridstream: preloaded stream link dead")

func (s *StreamManager) playPreloadedStream(ctx context.Context, opts *StartStreamOptions, cached *preloadedDebridStream) (err error) {
	defer util.HandlePanicInModuleWithError("debrid/client/playPreloadedStream", &err)

	// Mirror startStream: set both the per-user slot (for cancelStream) and the repository's
	// last-active slot (host/plugin accessors), under their respective locks.
	s.setPreviousStreamOptions(opts)
	s.repository.setPreviousStreamOptions(opts)
	s.repository.logger.Info().
		Int("mediaId", opts.MediaId).
		Str("playbackType", string(opts.PlaybackType)).
		Msgf("debridstream: Using preloaded stream for episode %s", opts.AniDBEpisode)

	s.cancelDownloadCtx()
	s.cancelPlaybackSubscriberCtx()

	// The native player needs an open session before we hand it the stream.
	if opts.PlaybackType == PlaybackTypeNativePlayer {
		s.ds(opts).BeginOpen(opts.ClientId, "Loading preloaded stream...", func() {
			// Keep the torrent on teardown so the shared prewarm cache can reuse it (see startStream).
			s.repository.CancelStream(&CancelStreamOptions{RemoveTorrent: false, UserID: opts.UserID})
		})
		if !s.ds(opts).IsOpenActive(opts.ClientId) {
			return nil
		}
	}

	s.setCurrentTorrentItemId(cached.torrentItemId)

	streamCtx, cancelCtx := context.WithCancel(context.Background())
	s.setDownloadCancel(cancelCtx)

	// Mirror the native-player open steps onto the debrid-state channel (the pill / mobile
	// overlay). Without this the pill is blank for a preloaded stream: its own "Adding/Downloading
	// torrent" events never fire here (the torrent was resolved during prewarm), so the loading
	// screen and the pill/server view drift apart.
	torrentName := "-"
	if cached.torrent != nil {
		torrentName = cached.torrent.Name
	}
	emitState := func(msg string) {
		if opts.PlaybackType != PlaybackTypeNativePlayer {
			return
		}
		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: torrentName,
			Message:     msg,
		})
	}
	emitState("Loading preloaded stream...")

	// Snapshot the mutable entry fields under the lock (refresh paths mutate them).
	s.preloadMu.Lock()
	streamUrl := cached.streamUrl
	urlResolvedAt := cached.urlResolvedAt
	torrentItemId := cached.torrentItemId
	s.preloadMu.Unlock()

	// dropDeadPreload releases a dead entry and reports the sentinel so startStream falls back
	// to a cold resolve — the click still plays, just cold. The open session stays alive (no
	// AbortOpen): the cold path re-enters it with its own steps.
	dropDeadPreload := func() error {
		s.preloadMu.Lock()
		delete(s.preloads, preloadKey(opts))
		if s.lastConsumedKey == preloadKey(opts) {
			s.lastConsumedKey = ""
		}
		s.preloadMu.Unlock()
		cancelCtx()
		s.setDownloadCancel(nil)
		if opts.PlaybackType == PlaybackTypeNativePlayer {
			s.ds(opts).UpdateOpenStep(opts.ClientId, "Link expired, re-resolving...")
			emitState("Link expired, re-resolving...")
		}
		s.invalidatePrewarmBadges(opts)
		s.repository.logger.Warn().Int("mediaId", opts.MediaId).Int("episode", opts.EpisodeNumber).
			Msg("debridstream: Preloaded link dead, falling back to cold resolve")
		return errPreloadedLinkDead
	}

	// Debrid stream URLs (CDN tokens) expire far sooner than the torrent. If this cached URL
	// is stale, re-resolve it from the already-added torrentItemId — cheap, and crucially it
	// does NOT call createtorrent (the 60/hour-limited endpoint), so the SELECTION stays cached
	// for a day while the URL is kept valid.
	if torrentItemId == "" && time.Since(urlResolvedAt) > urlRefreshTTL {
		// Direct-URL entry (no torrent item to re-resolve from): the only thing we can do with a
		// stale link is check it's alive. Dead → transparent cold-resolve fallback.
		if opts.PlaybackType == PlaybackTypeNativePlayer {
			s.ds(opts).UpdateOpenStep(opts.ClientId, "Checking stream link...")
			emitState("Checking stream link...")
		}
		if !probeStreamURL(ctx, streamUrl, prewarmProbeTimeoutPlay) {
			return dropDeadPreload()
		}
	}
	if torrentItemId != "" && time.Since(urlResolvedAt) > urlRefreshTTL {
		// This is the preload path's slow phase (provider status polls can retry for
		// seconds on API hiccups) — tell the player instead of sitting on one message.
		if opts.PlaybackType == PlaybackTypeNativePlayer {
			s.ds(opts).UpdateOpenStep(opts.ClientId, "Refreshing stream link...")
			emitState("Refreshing stream link...")
		}
		refreshed := false
		if provider, perr := s.repository.GetProvider(); perr == nil {
			itemCh := make(chan debrid.TorrentItem, 1)
			go func() {
				for range itemCh { //nolint:revive
				}
			}()
			freshUrl, rerr := provider.GetTorrentStreamUrl(streamCtx, debrid.StreamTorrentOptions{
				ID:     torrentItemId,
				FileId: cached.fileId,
			}, itemCh)
			close(itemCh)
			if rerr == nil && freshUrl != "" {
				streamUrl = freshUrl
				refreshed = true
				urlResolvedAt = time.Now()
				s.preloadMu.Lock()
				cached.streamUrl = freshUrl
				cached.urlResolvedAt = urlResolvedAt
				s.preloadMu.Unlock()
				s.repository.logger.Debug().Msg("debridstream: Refreshed expired preloaded URL (no createtorrent)")
				s.persistPrewarm(opts, cached) // share the refreshed URL to the account-wide DB
			} else if rerr != nil {
				s.repository.logger.Warn().Err(rerr).Msg("debridstream: Could not refresh preloaded URL")
			}
		}
		// Refresh failed → the stale cached link is the only candidate; verify it before
		// handing it to the player. Dead → cold fallback instead of a dead first frame.
		if !refreshed && !probeStreamURLWithSize(ctx, streamUrl, prewarmProbeTimeoutPlay, s.knownFileSizeFor(torrentItemId, cached.fileId)) {
			return dropDeadPreload()
		}
	}

	// Allow a hook to rewrite the stream URL / media (mirrors the normal resolve path).
	event := &DebridSendStreamToMediaPlayerEvent{
		WindowTitle:  "",
		StreamURL:    streamUrl,
		Media:        cached.media,
		AniDbEpisode: opts.AniDBEpisode,
		PlaybackType: string(opts.PlaybackType),
	}
	if err := hook.GlobalHookManager.OnDebridSendStreamToMediaPlayer().Trigger(event); err != nil {
		s.repository.logger.Err(err).Msg("debridstream: Failed to send preloaded stream to media player")
	}
	streamUrl = event.StreamURL
	media := event.Media

	if event.DefaultPrevented {
		s.repository.logger.Debug().Msg("debridstream: Preloaded stream prevented by hook")
		cancelCtx()
		s.setDownloadCancel(nil)
		return nil
	}

	s.setCurrentStreamUrl(streamUrl)
	s.setCurrentFileId(cached.fileId) // shareable selection for watch-room peers
	// Re-snapshot (URL may have been refreshed above) so a restart can replay it instantly.
	s.persistActiveStream(opts, streamUrl, torrentItemId, cached.fileId, cached.filepath, media, cached.torrent, urlResolvedAt)

	switch opts.PlaybackType {
	case PlaybackTypeNativePlayer:
		if !s.ds(opts).IsOpenActive(opts.ClientId) {
			return nil
		}
		s.ds(opts).UpdateOpenStep(opts.ClientId, "Preparing video...")
		emitState("Preparing video...")
		clientStreamUrl := ""
		if s.directCdnEligible(opts) {
			clientStreamUrl = s.resolveClientCdnUrl(cached.torrentItemId, cached.fileId, streamUrl)
		}
		err = s.ds(opts).PlayDebridStream(streamCtx, cached.filepath, directstream.PlayDebridStreamOptions{
			StreamUrl:       streamUrl,
			ClientStreamUrl: clientStreamUrl,
			MediaId:         media.ID,
			AnidbEpisode:    opts.AniDBEpisode,
			Media:           media,
			Torrent:         cached.torrent,
			FileId:          cached.fileId,
			UserAgent:       opts.UserAgent,
			ClientId:        opts.ClientId,
			AutoSelect:      false,
		})
		if err != nil {
			s.repository.logger.Error().Err(err).Msg("debridstream: Failed to play preloaded stream")
			return err
		}
		// Clear the pill: the native player's own loading screen ("Starting video...") now owns
		// the display, and without a terminal event the mirrored downloading-state would linger
		// and pop back as a stale pill once the loading screen unmounts.
		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusReady,
			TorrentName: torrentName,
			Message:     "Ready to stream the file",
		})

	case PlaybackTypeExternalPlayer:
		// The client (e.g. mobile mpv) opens this URL itself.
		s.ev(opts).SendEventTo(opts.ClientId, events.ExternalPlayerOpenURL, struct {
			Url           string `json:"url"`
			MediaId       int    `json:"mediaId"`
			EpisodeNumber int    `json:"episodeNumber"`
			MediaTitle    string `json:"mediaTitle"`
		}{
			Url:           streamUrl,
			MediaId:       opts.MediaId,
			EpisodeNumber: opts.EpisodeNumber,
			MediaTitle:    media.GetPreferredTitle(),
		})
		s.ev(opts).SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusReady,
			TorrentName: cached.torrent.Name,
			Message:     "External player link sent",
		})

	default:
		// Reuse is gated to the two types above; this is unreachable.
		s.repository.logger.Warn().Str("playbackType", string(opts.PlaybackType)).Msg("debridstream: Preloaded stream for unsupported playback type, ignoring")
		cancelCtx()
		s.setDownloadCancel(nil)
		return nil
	}

	go func() {
		defer util.HandlePanicInModuleThen("debridstream/AddBatchHistory", func() {})
		if cached.torrent != nil && cached.torrent.IsBatch {
			_ = db_bridge.InsertTorrentstreamHistory(s.repository.db, media.GetID(), cached.torrent, opts.BatchEpisodeFiles)
			s.ev(opts).SendEvent(events.InvalidateQueries, []string{events.GetTorrentstreamBatchHistoryEndpoint})
		}
	}()

	go s.chainNextEpisodePreload(opts, media)

	return nil
}

// chainNextEpisodePreload kicks a background preload of episode N+1 when a real auto-select
// stream of episode N starts. This is the event-driven feeder the 10-min tick can't be: the
// tick targets progress+1, but progress only syncs at 85%, so during a binge it kept
// re-resolving the episode already being watched and warmed the true next-up only by timing
// luck (~2/14 observed). Chaining fires on the actual play event, so it works for every
// client — including ones with no client-side preload trigger (Tenji, external players) —
// with zero client deploys. The web @3s client preload dedupes against it via the existing
// in-flight/fresh-key check in preloadStreamWith. Run on a goroutine (episode resolution may
// hit the metadata provider).
func (s *StreamManager) chainNextEpisodePreload(opts *StartStreamOptions, media *anilist.BaseAnime) {
	defer util.HandlePanicInModuleThen("debrid/client/chainNextEpisodePreload", func() {})
	if opts == nil || media == nil || opts.Preload || !opts.AutoSelect || !canReusePreloadedStream(opts.PlaybackType) {
		return
	}
	nextEp := opts.EpisodeNumber + 1
	if cnt := media.GetCurrentEpisodeCount(); cnt > 0 && nextEp > cnt {
		return // caught up to what's aired (or a movie) — no next episode to warm
	}
	// Resolve the real AniDB episode so the cache key matches what the client sends at play
	// time (differs from strconv(nextEp) for shows with specials / multiple seasons).
	aniDBEpisode := strconv.Itoa(nextEp)
	if ec, err := anime.NewEpisodeCollection(anime.NewEpisodeCollectionOptions{
		Media:               media,
		MetadataProviderRef: s.repository.metadataProviderRef,
		Logger:              s.repository.logger,
	}); err == nil {
		if ep, ok := ec.FindEpisodeByNumber(nextEp); ok && ep.AniDBEpisode != "" {
			aniDBEpisode = ep.AniDBEpisode
		}
	}
	_ = s.preloadStream(context.Background(), &StartStreamOptions{
		MediaId:       opts.MediaId,
		EpisodeNumber: nextEp,
		AniDBEpisode:  aniDBEpisode,
		UserID:        opts.UserID,
		AutoSelect:    true,
		Preload:       true,
		// The episode the user is watching right now is the highest-certainty target there is:
		// protect it from eviction and pre-parse its metadata (idempotent, CDN-budget-gated).
		Priority:        true,
		PrewarmMetadata: true,
	})
}

// fanOutBatchPreloads derives preload entries for the next episodes contained in the SAME
// already-added batch torrent — one cheap requestdl each, no search, no createtorrent. This is
// what turns "next episode instant" into "binge-ahead instant" within the TorBox 60/hr budget.
// Quality is untouched: the batch was chosen by the quality-first ranking for the base episode,
// and reusing it for its own sibling episodes is exactly what a manual batch play does.
func (s *StreamManager) fanOutBatchPreloads(
	ctx context.Context,
	base *StartStreamOptions,
	media *anilist.CompleteAnime,
	torrent *hibiketorrent.AnimeTorrent,
	torrentItemId string,
	otherFiles map[int]*debrid.TorrentItemFile,
) {
	defer util.HandlePanicInModuleThen("debrid/client/fanOutBatchPreloads", func() {})
	if base == nil || media == nil || torrent == nil || torrentItemId == "" || len(otherFiles) == 0 {
		return
	}
	provider, err := s.repository.GetProvider()
	if err != nil {
		return
	}
	baseMedia := media.ToBaseAnime()

	// Resolve real AniDB episode keys once, same as chainNextEpisodePreload — the derived
	// entries' keys must match what the client sends at play time.
	var ec *anime.EpisodeCollection
	if c, cerr := anime.NewEpisodeCollection(anime.NewEpisodeCollectionOptions{
		Media:               baseMedia,
		MetadataProviderRef: s.repository.metadataProviderRef,
		Logger:              s.repository.logger,
	}); cerr == nil {
		ec = c
	}

	for k := 1; k <= batchFanOutCount; k++ {
		if ctx.Err() != nil {
			return
		}
		ep := base.EpisodeNumber + k
		f, ok := otherFiles[ep]
		if !ok || f == nil {
			// A gap means the batch's numbering doesn't line up contiguously — stop rather
			// than guess across it.
			return
		}
		aniDBEpisode := strconv.Itoa(ep)
		if ec != nil {
			if e, found := ec.FindEpisodeByNumber(ep); found && e.AniDBEpisode != "" {
				aniDBEpisode = e.AniDBEpisode
			}
		}
		derived := &StartStreamOptions{
			MediaId:       base.MediaId,
			EpisodeNumber: ep,
			AniDBEpisode:  aniDBEpisode,
			UserID:        base.UserID,
			AutoSelect:    true,
			Preload:       true,
			Priority:      base.Priority,
		}
		key := preloadKey(derived)

		s.preloadMu.Lock()
		if existing, exists := s.preloads[key]; exists && time.Since(existing.resolvedAt) <= existing.ttl {
			s.preloadMu.Unlock()
			continue
		}
		if _, inflight := s.preloadInflight[key]; inflight {
			s.preloadMu.Unlock()
			continue
		}
		s.preloadMu.Unlock()

		itemCh := make(chan debrid.TorrentItem, 1)
		go func() {
			for range itemCh { //nolint:revive
			}
		}()
		streamUrl, uerr := provider.GetTorrentStreamUrl(ctx, debrid.StreamTorrentOptions{
			ID:     torrentItemId,
			FileId: f.ID,
		}, itemCh)
		close(itemCh)
		if uerr != nil || streamUrl == "" || ctx.Err() != nil {
			return
		}

		var stored *preloadedDebridStream
		s.preloadMu.Lock()
		if ctx.Err() == nil {
			s.evictIfNeededLocked(derived.Priority)
			delete(s.preloadFailedAt, key)
			now := time.Now()
			stored = &preloadedDebridStream{
				opts:          derived,
				streamUrl:     streamUrl,
				fileId:        f.ID,
				filepath:      f.Path,
				media:         baseMedia,
				torrent:       torrent,
				torrentItemId: torrentItemId,
				resolvedAt:    now,
				ttl:           selectionTTLForMedia(baseMedia),
				urlResolvedAt: now,
				priority:      derived.Priority,
			}
			s.preloads[key] = stored
		}
		s.preloadMu.Unlock()
		if stored != nil {
			s.repository.logger.Info().Int("episode", ep).Str("torrent", torrent.Name).Msg("debridstream: Batch fan-out preloaded sibling episode")
			s.persistPrewarm(derived, stored)
			s.invalidatePrewarmBadges(derived)
		}
	}
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
