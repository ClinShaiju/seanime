package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata_provider"
	"seanime/internal/continuity"
	discordrpc_presence "seanime/internal/discordrpc/presence"
	"seanime/internal/events"
	"seanime/internal/library/anime"
	"seanime/internal/mkvparser"
	"seanime/internal/nativeplayer"
	"seanime/internal/platforms/platform"
	"seanime/internal/util"
	httputil "seanime/internal/util/http"
	"seanime/internal/util/result"
	"seanime/internal/videocore"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/mo"
)

// Manager handles direct stream playback and progress tracking for the built-in video player.
// It is similar to playbackmanager.PlaybackManager.
type (
	Manager struct {
		Logger *zerolog.Logger

		// ------------ Modules ------------- //

		wsEventManager             events.WSEventManagerInterface
		continuityManager          *continuity.Manager
		metadataProviderRef        *util.Ref[metadata_provider.Provider]
		discordPresence            *discordrpc_presence.Presence
		platformRef                *util.Ref[platform.Platform]
		refreshAnimeCollectionFunc func()                                      // This function is called to refresh the AniList collection
		hmacTokenFunc              func(endpoint string, symbol string) string // Generates HMAC token query param for stream URLs

		nativePlayer *nativeplayer.NativePlayer

		videoCore           *videocore.VideoCore
		videoCoreSubscriber *videocore.Subscriber

		// --------- Playback Context -------- //

		playbackMu            sync.Mutex
		playbackCtx           context.Context
		playbackCtxCancelFunc context.CancelFunc

		// ---------- Playback State ---------- //

		currentStream          mo.Option[Stream] // The current stream being played
		currentPlaybackId      string
		currentPlaybackClient  string
		replacedPlaybackId     string
		replacedPlaybackClient string
		preparingClientID      string
		preparationCanceled    bool
		preparationCancelFunc  func()

		// \/ Stream playback
		// This is set by [SetStreamEpisodeCollection]
		currentStreamEpisodeCollection mo.Option[*anime.EpisodeCollection]

		settings *Settings

		isOfflineRef    *util.Ref[bool]
		animeCollection mo.Option[*anilist.AnimeCollection]
		animeCache      *result.Cache[int, *anilist.BaseAnime]

		parserCache *result.Cache[string, *mkvparser.MetadataParser]
		// streamInfoCache caches the content-type/length HEAD result by URL, so the play-time
		// LoadContentType (and prewarm) skip a ~1-2s round-trip to the debrid CDN.
		streamInfoCache *result.Cache[string, *StreamInfo]
		//playbackStatusSubscribers *result.Map[string, *PlaybackStatusSubscriber]
	}

	Settings struct {
		AutoPlayNextEpisode bool
		AutoUpdateProgress  bool
	}

	NewManagerOptions struct {
		Logger                     *zerolog.Logger
		WSEventManager             events.WSEventManagerInterface
		MetadataProviderRef        *util.Ref[metadata_provider.Provider]
		ContinuityManager          *continuity.Manager
		DiscordPresence            *discordrpc_presence.Presence
		PlatformRef                *util.Ref[platform.Platform]
		RefreshAnimeCollectionFunc func()
		IsOfflineRef               *util.Ref[bool]
		NativePlayer               *nativeplayer.NativePlayer
		VideoCore                  *videocore.VideoCore
		HMACTokenFunc              func(endpoint string, symbol string) string
	}
)

func NewManager(options NewManagerOptions) *Manager {
	ret := &Manager{
		Logger:                     options.Logger,
		wsEventManager:             options.WSEventManager,
		metadataProviderRef:        options.MetadataProviderRef,
		continuityManager:          options.ContinuityManager,
		discordPresence:            options.DiscordPresence,
		platformRef:                options.PlatformRef,
		refreshAnimeCollectionFunc: options.RefreshAnimeCollectionFunc,
		hmacTokenFunc:              options.HMACTokenFunc,
		isOfflineRef:               options.IsOfflineRef,
		currentStream:              mo.None[Stream](),
		nativePlayer:               options.NativePlayer,
		parserCache:                result.NewCache[string, *mkvparser.MetadataParser](),
		streamInfoCache:            result.NewCache[string, *StreamInfo](),
		videoCore:                  options.VideoCore,
	}
	ret.videoCoreSubscriber = ret.videoCore.Subscribe("directstream")
	ret.listenToPlayerEvents()

	// Disk hygiene: sweep orphaned filestream temp files left by abnormally-ended playbacks
	// (crash / hard kill) so they don't accumulate on disk. Runs once at startup.
	go httputil.CleanupStaleFileStreams(ret.Logger)

	return ret
}

// PrewarmStreamMetadata parses an HTTP stream's MKV metadata ahead of play and caches the parser
// by URL, so the "Loading metadata…" step on the real play is near-instant (loadPlaybackInfo reuses
// it). Zero disk — it uses a throwaway HTTP reader, no FileStream/playback cache. Best-effort:
// failures are ignored and the normal path parses fresh. Skips work if already cached.
//
// Memory note: GetMetadata loads font attachments into RAM (tens of MB). Callers should only invoke
// this for high-certainty targets (the next episode), not for every speculative prewarm.
func (m *Manager) PrewarmStreamMetadata(streamUrl string) {
	defer util.HandlePanicInModuleThen("directstream/PrewarmStreamMetadata", func() {})
	if streamUrl == "" {
		return
	}
	// Prewarm the content-type/length HEAD too — otherwise the play still pays a ~1-2s CDN
	// round-trip in LoadContentType even when the metadata parse is cached.
	info, _ := m.FetchStreamInfoWithHeaders(streamUrl, nil)

	if _, ok := m.parserCache.Get(streamUrl); ok {
		return // already parsed
	}
	reader, err := httputil.NewHttpReadSeekerFromURLWithHeaders(streamUrl, nil)
	if err != nil {
		return
	}
	parser := mkvparser.NewMetadataParser(reader, m.Logger)
	md := parser.GetMetadata(context.Background())
	_ = reader.Close() // metadata (incl. attachment bytes) is now in RAM; mirrors loadPlaybackInfo
	if md == nil || md.Error != nil {
		return
	}
	m.parserCache.SetT(streamUrl, parser, 15*time.Minute)
	m.Logger.Debug().Str("url", streamUrl).Msg("directstream: Prewarmed stream metadata")

	// Warm the playable-start region on the debrid CDN so the play-time first range fetch isn't a
	// cold round-trip. Throwaway read — zero local disk, just primes the CDN edge / wakes the file.
	warmBytes := int64(warmFallbackBytes)
	if info != nil && md != nil {
		warmBytes = computeWarmBytes(info.ContentLength, md.Duration)
	}
	go warmStreamStart(streamUrl, warmBytes)
}

const (
	// warmSeconds is how many seconds of video to warm at the start (bitrate-scaled, so a high-
	// bitrate GB-sized episode warms proportionally more bytes than a small one).
	warmSeconds = 6
	// Bounds on the warm size. The floor must reliably cover the first video keyframe — observed at
	// ~5.5MB on a low-bitrate release, where 6s-of-average-bitrate underestimated it — plus a little
	// buffer. The ceiling caps a high-bitrate release so it doesn't pull tens of MB per prewarm.
	warmMinBytes      = 16 * 1024 * 1024
	warmMaxBytes      = 48 * 1024 * 1024
	warmFallbackBytes = 16 * 1024 * 1024
)

// computeWarmBytes returns ~warmSeconds of video for the given file, clamped to [warmMin, warmMax].
func computeWarmBytes(contentLength int64, durationSec float64) int64 {
	if contentLength <= 0 || durationSec <= 1 {
		return warmFallbackBytes
	}
	n := int64(float64(contentLength) / durationSec * warmSeconds) // bytes/sec × seconds
	if n < warmMinBytes {
		n = warmMinBytes
	}
	if n > warmMaxBytes {
		n = warmMaxBytes
	}
	if n > contentLength {
		n = contentLength
	}
	return n
}

func warmStreamStart(streamUrl string, warmBytes int64) {
	defer util.HandlePanicInModuleThen("directstream/warmStreamStart", func() {})
	if warmBytes <= 0 {
		return
	}
	req, err := http.NewRequest(http.MethodGet, streamUrl, nil)
	if err != nil {
		return
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", warmBytes-1))
	resp, err := videoProxyClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, warmBytes)
}

func (m *Manager) SetAnimeCollection(ac *anilist.AnimeCollection) {
	m.animeCollection = mo.Some(ac)
}

func (m *Manager) SetSettings(s *Settings) {
	m.settings = s
}

// GetHMACTokenQueryParam returns an HMAC token query param for the given endpoint, or empty string if not available.
func (m *Manager) GetHMACTokenQueryParam(endpoint string, symbol string) string {
	if m.hmacTokenFunc != nil {
		return m.hmacTokenFunc(endpoint, symbol)
	}
	return ""
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (m *Manager) getAnime(ctx context.Context, mediaId int) (*anilist.BaseAnime, error) {
	media, ok := m.animeCache.Get(mediaId)
	if ok {
		return media, nil
	}

	// Find in anime collection
	animeCollection, ok := m.animeCollection.Get()
	if ok {
		media, ok := animeCollection.FindAnime(mediaId)
		if ok {
			return media, nil
		}
	}

	// Find in platform
	media, err := m.platformRef.Get().GetAnime(ctx, mediaId)
	if err != nil {
		return nil, err
	}

	// Cache
	m.animeCache.SetT(mediaId, media, 1*time.Hour)

	return media, nil
}
