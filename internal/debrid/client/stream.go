package debrid_client

import (
	"context"
	"errors"
	"fmt"
	"seanime/internal/api/anilist"
	"seanime/internal/database/db_bridge"
	"seanime/internal/debrid/debrid"
	"seanime/internal/directstream"
	"seanime/internal/events"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/hook"
	"seanime/internal/library/playbackmanager"
	"seanime/internal/util"
	"strconv"
	"sync"
	"time"

	"github.com/samber/mo"
)

type (
	StreamManager struct {
		repository            *Repository
		currentTorrentItemId  string
		downloadCtxCancelFunc context.CancelFunc

		currentStreamUrl string

		playbackSubscriberCtxCancelFunc context.CancelFunc

		// Preloaded streams, resolved ahead of time so playback starts instantly. Keyed by
		// media+episode (preloadKey). Holds the ~80% next-episode preload plus the server-side
		// continue-watching prewarm (next-up of the last few shows watched).
		preloadMu       sync.Mutex
		preloads        map[string]*preloadedDebridStream // resolved, ready to consume
		preloadInflight map[string]context.CancelFunc     // in-flight resolves, for dedupe/cancel
		// lastConsumedKey is the preload entry currently being played. Kept (not deleted) on
		// consume so re-pressing/restarting the same episode is instant; deleted on episode end
		// (a different episode starts, or the stream is cancelled). TTL is the staleness backstop.
		lastConsumedKey string
	}

	// preloadedDebridStream holds a fully-resolved debrid stream URL for a future episode.
	preloadedDebridStream struct {
		opts          *StartStreamOptions
		streamUrl     string
		fileId        string
		filepath      string
		media         *anilist.BaseAnime
		torrent       *hibiketorrent.AnimeTorrent
		torrentItemId string
		resolvedAt    time.Time // for TTL eviction; debrid URLs expire
		priority      bool      // protected from eviction over speculative hover prewarms
	}

	StreamPlaybackType string

	StreamStatus string

	StreamState struct {
		Status      StreamStatus `json:"status"`
		TorrentName string       `json:"torrentName"`
		Message     string       `json:"message"`
	}

	StartStreamOptions struct {
		MediaId           int
		EpisodeNumber     int                         // RELATIVE Episode number to identify the file
		AniDBEpisode      string                      // Anizip episode
		Torrent           *hibiketorrent.AnimeTorrent // Selected torrent
		FileId            string                      // File ID or index
		FileIndex         *int                        // Index of the file to stream (Manual selection)
		UserAgent         string
		ClientId          string
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
		repository:           repository,
		currentTorrentItemId: "",
		preloads:             make(map[string]*preloadedDebridStream),
		preloadInflight:      make(map[string]context.CancelFunc),
	}
}

const (
	// preloadTTL bounds how long a resolved stream URL is trusted before we re-resolve.
	preloadTTL = 15 * time.Minute
	// maxSpeculativePreloads caps ONLY the speculative browse/search/discover hover prewarms.
	// The continue-watching (priority) entries — the set the user actually clicks — are uncapped:
	// they're bounded at the source (3 shows + next-ep) and self-expire via TTL, so the speculative
	// hover firehose can never evict them.
	maxSpeculativePreloads = 8
)

// preloadKey identifies a preload slot by the episode it targets.
func preloadKey(opts *StartStreamOptions) string {
	return fmt.Sprintf("%d|%d|%s", opts.MediaId, opts.EpisodeNumber, opts.AniDBEpisode)
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
	if s.repository.sessionModulesFunc != nil && opts != nil {
		if dm, _ := s.repository.sessionModulesFunc(opts.UserID); dm != nil {
			return dm
		}
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

// startStream is called by the client to start streaming a torrent
func (s *StreamManager) startStream(ctx context.Context, opts *StartStreamOptions) (err error) {
	defer util.HandlePanicInModuleWithError("debrid/client/StartStream", &err)

	// Reuse a preloaded stream if one matches this episode (native player + external player link).
	if canReusePreloadedStream(opts.PlaybackType) {
		key := preloadKey(opts)
		s.preloadMu.Lock()
		// A different episode is starting → the previously-consumed one has ended; drop its kept
		// entry (replays of the SAME episode keep theirs, so this leaves a same-key hit intact).
		if s.lastConsumedKey != "" && s.lastConsumedKey != key {
			delete(s.preloads, s.lastConsumedKey)
			s.lastConsumedKey = ""
		}
		cached, ok := s.preloads[key]
		fresh := ok && time.Since(cached.resolvedAt) <= preloadTTL
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
		if hit {
			return s.playPreloadedStream(ctx, opts, cached)
		}
	}

	s.repository.previousStreamOptions = mo.Some(opts)

	s.repository.logger.Info().
		Str("clientId", opts.ClientId).
		Any("playbackType", opts.PlaybackType).
		Int("mediaId", opts.MediaId).Msgf("debridstream: Starting stream for episode %s", opts.AniDBEpisode)

	// Cancel the download context if it's running
	if s.downloadCtxCancelFunc != nil {
		s.downloadCtxCancelFunc()
		s.downloadCtxCancelFunc = nil
	}

	if s.playbackSubscriberCtxCancelFunc != nil {
		s.playbackSubscriberCtxCancelFunc()
		s.playbackSubscriberCtxCancelFunc = nil
	}

	provider, err := s.repository.GetProvider()
	if err != nil {
		return fmt.Errorf("debridstream: Failed to start stream: %w", err)
	}

	s.repository.wsEventManager.SendEvent(events.ShowIndefiniteLoader, "debridstream")
	//defer func() {
	//	s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
	//}()

	if opts.PlaybackType == PlaybackTypeNativePlayer {
		s.ds(opts).BeginOpen(opts.ClientId, "Selecting torrent...", func() {
			s.repository.CancelStream(&CancelStreamOptions{RemoveTorrent: true})
		})
	}

	//
	// Get the media info
	//
	media, _, err := s.getMediaInfo(ctx, opts.MediaId)
	if err != nil {
		s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
		return err
	}
	if opts.PlaybackType == PlaybackTypeNativePlayer && !s.ds(opts).IsOpenActive(opts.ClientId) {
		s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
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

		s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: "-",
			Message:     "Selecting best torrent...",
		})

		pt, err := s.repository.findBestTorrent(provider, media, opts.EpisodeNumber, opts.UserID)
		if err != nil {
			if opts.PlaybackType == PlaybackTypeNativePlayer {
				s.ds(opts).AbortOpen(opts.ClientId, err)
			}
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusFailed,
				TorrentName: "-",
				Message:     fmt.Sprintf("Failed to select best torrent, %v", err),
			})
			s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
			return fmt.Errorf("debridstream: Failed to start stream: %w", err)
		}
		selectedTorrent = pt.torrent
		fileId = pt.fileId
		filepath = pt.filepath
		directStreamUrl = pt.streamUrl
	} else {
		// Manual selection
		if selectedTorrent == nil {
			s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
			return fmt.Errorf("debridstream: Failed to start stream, no torrent provided")
		}

		s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: selectedTorrent.Name,
			Message:     "Analyzing selected torrent...",
		})

		if selectedTorrent.StreamUrl != "" {
			// Pre-resolved direct stream — nothing to analyze.
			directStreamUrl = selectedTorrent.StreamUrl
			filepath = selectedTorrent.Name
		} else if fileId == "" {
			// If no fileId is provided, we need to analyze the torrent to find the correct file
			var chosenFileIndex *int
			if opts.FileIndex != nil {
				chosenFileIndex = opts.FileIndex
			}
			pt, err := s.repository.findBestTorrentFromManualSelection(provider, selectedTorrent, media, opts.EpisodeNumber, chosenFileIndex)
			if err != nil {
				if opts.PlaybackType == PlaybackTypeNativePlayer {
					s.ds(opts).AbortOpen(opts.ClientId, err)
				}
				s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
					Status:      StreamStatusFailed,
					TorrentName: selectedTorrent.Name,
					Message:     fmt.Sprintf("Failed to analyze torrent, %v", err),
				})
				s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
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
		s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
		return fmt.Errorf("debridstream: Failed to start stream, no torrent provided")
	}
	if opts.PlaybackType == PlaybackTypeNativePlayer && !s.ds(opts).IsOpenActive(opts.ClientId) {
		s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
		return nil
	}

	// Pre-resolved direct streams have no torrent to add — torrentItemId stays empty and the
	// goroutine below uses directStreamUrl instead of polling GetTorrentStreamUrl.
	torrentItemId := ""
	if directStreamUrl == "" {
		s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusDownloading,
			TorrentName: selectedTorrent.Name,
			Message:     "Adding torrent...",
		})

		// Add the torrent to the debrid service
		torrentItemId, err = provider.AddTorrent(debrid.AddTorrentOptions{
			MagnetLink:   selectedTorrent.MagnetLink,
			InfoHash:     selectedTorrent.InfoHash,
			SelectFileId: fileId, // RD-only, download only the selected file
		})
		if err != nil {
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusFailed,
				TorrentName: selectedTorrent.Name,
				Message:     fmt.Sprintf("Failed to add torrent, %v", err),
			})
			s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
			return fmt.Errorf("debridstream: Failed to add torrent: %w", err)
		}

		// ponytail: no settle needed — GetTorrentStreamUrl's first poll is now 500ms out (with
		// backoff), which is plenty for the just-added item to become queryable.
	}

	// Save the current torrent item id
	s.currentTorrentItemId = torrentItemId
	ctx, cancelCtx := context.WithCancel(context.Background())
	s.downloadCtxCancelFunc = cancelCtx

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
			s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
		}()

		defer func() {
			// Cancel the context
			if s.downloadCtxCancelFunc != nil {
				s.downloadCtxCancelFunc()
				s.downloadCtxCancelFunc = nil
			}
		}()

		s.repository.logger.Debug().Msg("debridstream: Listening to torrent status")

		var streamUrl string
		if directStreamUrl != "" {
			// Pre-resolved direct stream — no download to await.
			streamUrl = directStreamUrl
		} else {
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusDownloading,
				TorrentName: selectedTorrent.Name,
				Message:     fmt.Sprintf("Downloading torrent..."),
			})

			itemCh := make(chan debrid.TorrentItem, 1)

			go func() {
				for item := range itemCh {
					if opts.PlaybackType == PlaybackTypeNativePlayer {
						if !s.ds(opts).UpdateOpenStep(opts.ClientId, fmt.Sprintf("Awaiting stream: %d%%", item.CompletionPercentage)) {
							return
						}
					}

					s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
						Status:      StreamStatusDownloading,
						TorrentName: item.Name,
						Message:     fmt.Sprintf("Downloading torrent: %d%%", item.CompletionPercentage),
					})
				}
			}()

			// Await the stream URL
			// For Torbox, this will wait until the entire torrent is downloaded
			url, err := provider.GetTorrentStreamUrl(ctx, debrid.StreamTorrentOptions{
				ID:     torrentItemId,
				FileId: fileId,
			}, itemCh)

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
					s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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
			s.repository.logger.Debug().Msg("debridstream: Stream URL received, checking stream file")
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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

							s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
								Status:      StreamStatusFailed,
								TorrentName: selectedTorrent.Name,
								Message:     fmt.Sprintf("Cannot stream this file: %s", reason),
							})
							return
						}
						s.repository.logger.Warn().Msg("debridstream: Rechecking stream file in 8 seconds")
						s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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
		}

		s.repository.logger.Debug().Msg("debridstream: Stream is ready")

		// Signal to the client that the torrent is ready to stream
		s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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

		s.currentStreamUrl = streamUrl

		switch playbackType {
		case PlaybackTypeNone:
			// No playback type selected, just signal to the client that the stream is ready
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "External player link sent",
			})
		case PlaybackTypeNoneAndAwait:
			// No playback type selected, just signal to the client that the stream is ready
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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

			s.repository.wsEventManager.SendEvent(events.InfoToast, "Sending stream to media player...")
			s.repository.wsEventManager.SendEvent(events.ShowIndefiniteLoader, "debridstream")

			var playbackSubscriberCtx context.Context
			playbackSubscriberCtx, s.playbackSubscriberCtxCancelFunc = context.WithCancel(context.Background())
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
				if s.playbackSubscriberCtxCancelFunc != nil {
					s.playbackSubscriberCtxCancelFunc()
					s.playbackSubscriberCtxCancelFunc = nil
				}
				// Failed to start the stream, we'll drop the torrents and stop the server
				s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
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
				defer func() {
					if s.playbackSubscriberCtxCancelFunc != nil {
						s.playbackSubscriberCtxCancelFunc()
						s.playbackSubscriberCtxCancelFunc = nil
					}
				}()
				select {
				case <-playbackSubscriberCtx.Done():
					s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
					s.pb(opts).UnsubscribeFromPlaybackStatus("debridstream")
					s.currentStreamUrl = ""
				case event := <-playbackSubscriber.EventCh:
					switch event.(type) {
					case playbackmanager.StreamStartedEvent:
						s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
					case playbackmanager.StreamStoppedEvent:
						go s.pb(opts).UnsubscribeFromPlaybackStatus("debridstream")
						s.currentStreamUrl = ""
					}
				}
			}()

		case PlaybackTypeExternalPlayer:
			// Send the external player link
			s.repository.wsEventManager.SendEventTo(opts.ClientId, events.ExternalPlayerOpenURL, struct {
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
			s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
				Status:      StreamStatusReady,
				TorrentName: selectedTorrent.Name,
				Message:     "External player link sent",
			})
		case PlaybackTypeNativePlayer:
			if !s.ds(opts).IsOpenActive(opts.ClientId) {
				return
			}
			err := s.ds(opts).PlayDebridStream(ctx, filepath, directstream.PlayDebridStreamOptions{
				StreamUrl:    streamUrl,
				MediaId:      media.ID,
				AnidbEpisode: opts.AniDBEpisode,
				Media:        media,
				Torrent:      selectedTorrent,
				FileId:       fileId,
				UserAgent:    opts.UserAgent,
				ClientId:     opts.ClientId,
				AutoSelect:   false,
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

				s.repository.wsEventManager.SendEvent(events.InvalidateQueries, []string{events.GetTorrentstreamBatchHistoryEndpoint})
			}
		}()
	}(ctx)

	s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
		Status:      StreamStatusStarted,
		TorrentName: selectedTorrent.Name,
		Message:     "Stream started",
	})
	s.repository.logger.Info().Msg("debridstream: Stream started")

	if opts.PlaybackType == PlaybackTypeNoneAndAwait {
		s.repository.logger.Debug().Msg("debridstream: Waiting for stream to be ready")
		<-readyCh
		s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")
	}

	return nil
}

func (s *StreamManager) cancelStream(opts *CancelStreamOptions) {
	// The playing episode is ending → drop only ITS kept preload entry (the one held for instant
	// replay). Other shows' continue-watching prewarms stay warm; stale entries self-evict via TTL.
	// Full reset (provider change/shutdown) uses ClearAllPreloads.
	s.preloadMu.Lock()
	if s.lastConsumedKey != "" {
		delete(s.preloads, s.lastConsumedKey)
		s.lastConsumedKey = ""
	}
	s.preloadMu.Unlock()

	// Resolve the directStream of the user who owns the stream being cancelled.
	var prevOpts *StartStreamOptions
	if p, ok := s.repository.previousStreamOptions.Get(); ok {
		prevOpts = p
	}
	if dm := s.ds(prevOpts); dm != nil {
		dm.CloseOpen("")
	}

	if s.downloadCtxCancelFunc != nil {
		s.downloadCtxCancelFunc()
		s.downloadCtxCancelFunc = nil
	}

	s.repository.wsEventManager.SendEvent(events.HideIndefiniteLoader, "debridstream")

	s.currentStreamUrl = ""

	if opts.RemoveTorrent && s.currentTorrentItemId != "" {
		// Remove the torrent from the debrid service
		provider, err := s.repository.GetProvider()
		if err != nil {
			s.repository.logger.Err(err).Msg("debridstream: Failed to remove torrent")
			return
		}

		// Remove the torrent from the debrid service
		err = provider.DeleteTorrent(s.currentTorrentItemId)
		if err != nil {
			s.repository.logger.Err(err).Msg("debridstream: Failed to remove torrent")
		}
	}
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
	s.lastConsumedKey = ""
}

// evictIfNeededLocked enforces the speculative-preload budget. Continue-watching (priority) entries
// are NOT capped here — they're bounded at the source (3 shows + next-ep) and self-expire via TTL —
// so the browse/search/discover hover firehose can never evict them. Caller holds preloadMu.
func (s *StreamManager) evictIfNeededLocked(priority bool) {
	// Drop any TTL-expired entries first (either class) so the map can't grow unbounded over a binge.
	for k, v := range s.preloads {
		if time.Since(v.resolvedAt) > preloadTTL {
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
			if v.priority {
				continue // never count or evict priority entries
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
func (s *StreamManager) preloadStream(ctx context.Context, opts *StartStreamOptions) (err error) {
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
	if existing, ok := s.preloads[key]; ok && time.Since(existing.resolvedAt) <= preloadTTL {
		// Upgrade a speculative entry to priority if a continue-watching prewarm arrives for it,
		// so an earlier hover/entry-mount preload of the same episode can't leave it evictable.
		if opts.Priority && !existing.priority {
			existing.priority = true
		}
		s.preloadMu.Unlock()
		return nil
	}
	if _, inflight := s.preloadInflight[key]; inflight {
		s.preloadMu.Unlock()
		return nil
	}
	preloadCtx, cancel := context.WithCancel(context.Background())
	s.preloadInflight[key] = cancel
	s.preloadMu.Unlock()

	s.repository.logger.Info().
		Int("mediaId", opts.MediaId).
		Int("episodeNumber", opts.EpisodeNumber).
		Msg("debridstream: Preloading stream")

	go func() {
		defer util.HandlePanicInModuleThen("debrid/client/preloadStream", func() {})
		defer func() {
			s.preloadMu.Lock()
			delete(s.preloadInflight, key)
			s.preloadMu.Unlock()
		}()

		media, _, err := s.getMediaInfo(preloadCtx, opts.MediaId)
		if err != nil || preloadCtx.Err() != nil {
			return
		}

		selectedTorrent := opts.Torrent
		fileId := opts.FileId
		filepath := ""
		directStreamUrl := ""

		if opts.AutoSelect {
			pt, err := s.repository.findBestTorrent(provider, media, opts.EpisodeNumber, opts.UserID)
			if err != nil {
				s.repository.logger.Warn().Err(err).Msg("debridstream: Preload failed to select torrent")
				return
			}
			selectedTorrent, fileId, filepath, directStreamUrl = pt.torrent, pt.fileId, pt.filepath, pt.streamUrl
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

		s.preloadMu.Lock()
		// Only store if this preload wasn't superseded or cancelled in the meantime.
		if preloadCtx.Err() == nil {
			s.evictIfNeededLocked(opts.Priority)
			s.preloads[key] = &preloadedDebridStream{
				opts:          opts,
				streamUrl:     streamUrl,
				fileId:        fileId,
				filepath:      filepath,
				media:         media.ToBaseAnime(),
				torrent:       selectedTorrent,
				torrentItemId: torrentItemId,
				resolvedAt:    time.Now(),
				priority:      opts.Priority,
			}
			s.repository.logger.Info().Str("torrent", selectedTorrent.Name).Msg("debridstream: Preloaded stream ready")
		}
		s.preloadMu.Unlock()

		// Pre-parse MKV metadata for high-certainty targets (next-episode preloads) so the
		// play-time "Loading metadata" step is near-instant. Zero disk; gated by PrewarmMetadata
		// so the speculative continue-watching prewarm doesn't download fonts it may never use.
		if opts.PrewarmMetadata && streamUrl != "" && preloadCtx.Err() == nil && s.ds(opts) != nil {
			s.ds(opts).PrewarmStreamMetadata(streamUrl)
		}
	}()

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
func (s *StreamManager) playPreloadedStream(ctx context.Context, opts *StartStreamOptions, cached *preloadedDebridStream) (err error) {
	defer util.HandlePanicInModuleWithError("debrid/client/playPreloadedStream", &err)

	s.repository.previousStreamOptions = mo.Some(opts)
	s.repository.logger.Info().
		Int("mediaId", opts.MediaId).
		Str("playbackType", string(opts.PlaybackType)).
		Msgf("debridstream: Using preloaded stream for episode %s", opts.AniDBEpisode)

	if s.downloadCtxCancelFunc != nil {
		s.downloadCtxCancelFunc()
		s.downloadCtxCancelFunc = nil
	}
	if s.playbackSubscriberCtxCancelFunc != nil {
		s.playbackSubscriberCtxCancelFunc()
		s.playbackSubscriberCtxCancelFunc = nil
	}

	// The native player needs an open session before we hand it the stream.
	if opts.PlaybackType == PlaybackTypeNativePlayer {
		s.ds(opts).BeginOpen(opts.ClientId, "Loading preloaded stream...", func() {
			s.repository.CancelStream(&CancelStreamOptions{RemoveTorrent: true})
		})
		if !s.ds(opts).IsOpenActive(opts.ClientId) {
			return nil
		}
	}

	s.currentTorrentItemId = cached.torrentItemId

	streamCtx, cancelCtx := context.WithCancel(context.Background())
	s.downloadCtxCancelFunc = cancelCtx

	// Allow a hook to rewrite the stream URL / media (mirrors the normal resolve path).
	streamUrl := cached.streamUrl
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
		s.downloadCtxCancelFunc = nil
		return nil
	}

	s.currentStreamUrl = streamUrl

	switch opts.PlaybackType {
	case PlaybackTypeNativePlayer:
		if !s.ds(opts).IsOpenActive(opts.ClientId) {
			return nil
		}
		err = s.ds(opts).PlayDebridStream(streamCtx, cached.filepath, directstream.PlayDebridStreamOptions{
			StreamUrl:    streamUrl,
			MediaId:      media.ID,
			AnidbEpisode: opts.AniDBEpisode,
			Media:        media,
			Torrent:      cached.torrent,
			FileId:       cached.fileId,
			UserAgent:    opts.UserAgent,
			ClientId:     opts.ClientId,
			AutoSelect:   false,
		})
		if err != nil {
			s.repository.logger.Error().Err(err).Msg("debridstream: Failed to play preloaded stream")
			return err
		}

	case PlaybackTypeExternalPlayer:
		// The client (e.g. mobile mpv) opens this URL itself.
		s.repository.wsEventManager.SendEventTo(opts.ClientId, events.ExternalPlayerOpenURL, struct {
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
		s.repository.wsEventManager.SendEvent(events.DebridStreamState, StreamState{
			Status:      StreamStatusReady,
			TorrentName: cached.torrent.Name,
			Message:     "External player link sent",
		})

	default:
		// Reuse is gated to the two types above; this is unreachable.
		s.repository.logger.Warn().Str("playbackType", string(opts.PlaybackType)).Msg("debridstream: Preloaded stream for unsupported playback type, ignoring")
		cancelCtx()
		s.downloadCtxCancelFunc = nil
		return nil
	}

	go func() {
		defer util.HandlePanicInModuleThen("debridstream/AddBatchHistory", func() {})
		if cached.torrent != nil && cached.torrent.IsBatch {
			_ = db_bridge.InsertTorrentstreamHistory(s.repository.db, media.GetID(), cached.torrent, opts.BatchEpisodeFiles)
			s.repository.wsEventManager.SendEvent(events.InvalidateQueries, []string{events.GetTorrentstreamBatchHistoryEndpoint})
		}
	}()

	return nil
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
