package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"seanime/internal/library/anime"
	"seanime/internal/mkvparser"
	"seanime/internal/nativeplayer"
	httputil "seanime/internal/util/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/samber/mo"
)

// httpBaseStream holds shared state and logic for HTTP URL-based streams (debrid, URL, nakama).
type httpBaseStream struct {
	BaseStream
	streamUrl           string
	contentLength       int64
	filepath            string
	requestHeaders      http.Header
	headResponseHeaders http.Header
	httpStream          *httputil.FileStream // Shared file-backed cache for multiple readers
	cacheMu             sync.RWMutex         // Protects httpStream access
}

var videoProxyClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		// Bound concurrent connections to a single CDN host so a burst of range requests
		// (stream start + metadata + a seek) can't hammer the debrid CDN into 429ing the
		// token. Excess requests queue rather than fail. Generous enough for several
		// concurrent streams at this deployment's scale; tune if it ever blocks legit reads.
		MaxConnsPerHost:     8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false, // Fixes issues on Linux
	},
}

const maxCDNRetries = 4

// isCDNTransientStatus reports whether a CDN status is worth retrying — rate-limiting
// (429) or transient gateway errors. Permanent 4xx (403/404/416) are not retried.
func isCDNTransientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// cdnBackoffDuration is the wait before retrying a throttled CDN request: a numeric
// Retry-After (seconds, capped at 5s) when the CDN sends one, else exponential backoff
// (300ms, 600ms, 1.2s… capped at 3s). Pure, so it's unit-testable.
func cdnBackoffDuration(attempt int, retryAfter string) time.Duration {
	wait := time.Duration(300*(1<<uint(attempt))) * time.Millisecond
	if wait > 3*time.Second {
		wait = 3 * time.Second
	}
	if retryAfter != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
			wait = time.Duration(secs) * time.Second
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
		}
	}
	return wait
}

// cdnRetryWait sleeps for the backoff, aborting early if the client disconnects.
// Returns false if the context was cancelled while waiting.
func cdnRetryWait(ctx context.Context, attempt int, retryAfter string) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(cdnBackoffDuration(attempt, retryAfter)):
		return true
	}
}

// Headers that should not be forwarded to the CDN
var proxyHopHeaders = map[string]bool{
	"Host":                true,
	"Accept":              true,
	"Accept-Encoding":     true,
	"Range":               true,
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func (s *httpBaseStream) applyReqHeaders(dst http.Header) {
	overrideHeaders(dst, s.requestHeaders)
}

func (s *httpBaseStream) applyHeadRespHeaders(dst http.Header) {
	overrideHeaders(dst, s.headResponseHeaders)
}

func (s *httpBaseStream) newMetadataReader() (io.ReadSeekCloser, error) {
	return httputil.NewHttpReadSeekerFromURLWithHeaders(s.streamUrl, s.requestHeaders)
}

func (s *httpBaseStream) LoadContentType() string {
	s.contentTypeOnce.Do(func() {
		s.cacheMu.RLock()
		if s.httpStream == nil {
			s.cacheMu.RUnlock()
			_ = s.initializeStream()
		} else {
			s.cacheMu.RUnlock()
		}

		info, ok := s.manager.FetchStreamInfoWithHeaders(s.streamUrl, s.requestHeaders)
		if !ok {
			s.logger.Warn().Str("url", s.streamUrl).Msg("directstream(http): Failed to fetch stream info for content type")
			return
		}
		s.logger.Debug().Str("url", s.streamUrl).Str("contentType", info.ContentType).Int64("contentLength", info.ContentLength).Msg("directstream(http): Fetched content type and length")
		s.contentType = info.ContentType
		if s.contentType == "application/force-download" {
			s.contentType = "application/octet-stream"
		}
		s.contentLength = info.ContentLength
	})

	return s.contentType
}

// Close cleans up the HTTP cache and other resources
func (s *httpBaseStream) Close() error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	s.logger.Debug().Msg("directstream(http): Closing HTTP cache")

	if s.httpStream != nil {
		if err := s.httpStream.Close(); err != nil {
			s.logger.Error().Err(err).Msg("directstream(http): Failed to close HTTP cache")
			return err
		}
		s.httpStream = nil
	}

	s.logger.Debug().Msg("directstream(http): HTTP cache closed successfully")

	return nil
}

// Terminate overrides BaseStream.Terminate to also clean up the HTTP cache
func (s *httpBaseStream) Terminate() {
	// Clean up HTTP cache first
	if err := s.Close(); err != nil {
		s.logger.Error().Err(err).Msg("directstream(http): Failed to clean up HTTP cache during termination")
	}

	// Call the base implementation
	s.BaseStream.Terminate()
}

// loadPlaybackInfo is called by concrete types, passing their own StreamType.
func (s *httpBaseStream) loadPlaybackInfo(streamType nativeplayer.StreamType) (ret *nativeplayer.PlaybackInfo, err error) {
	s.playbackInfoOnce.Do(func() {
		if s.streamUrl == "" {
			ret = &nativeplayer.PlaybackInfo{}
			err = fmt.Errorf("stream url is not set")
			s.playbackInfoErr = err
			return
		}

		id := uuid.New().String()

		var entryListData *anime.EntryListData
		if animeCollection, ok := s.manager.animeCollection.Get(); ok {
			if listEntry, ok := animeCollection.GetListEntryFromAnimeId(s.media.ID); ok {
				entryListData = anime.NewEntryListData(listEntry)
			}
		}

		contentType := s.LoadContentType()

		playbackInfo := nativeplayer.PlaybackInfo{
			ID:                id,
			StreamType:        streamType,
			StreamPath:        s.filepath,
			MimeType:          contentType,
			StreamUrl:         "{{SERVER_URL}}/api/v1/directstream/stream?id=" + id + s.manager.GetHMACTokenQueryParam("/api/v1/directstream/stream", "&"),
			ContentLength:     s.contentLength, // loaded by LoadContentType
			MkvMetadata:       nil,
			MkvMetadataParser: mo.None[*mkvparser.MetadataParser](),
			Episode:           s.episode,
			Media:             s.media,
			EntryListData:     entryListData,
		}

		// If the content type is an EBML content type, we can create a metadata parser
		if isEbmlContent(s.LoadContentType()) || s.LoadContentType() == "application/octet-stream" || s.LoadContentType() == "application/force-download" {
			// Reuse a prewarmed parser for this URL if one exists — its GetMetadata is sync.Once
			// cached, so this skips the ~2-3s parse (font download) entirely. Subtitle/attachment
			// serving creates its own readers, so the parser's closed original reader is irrelevant.
			if cached, ok := s.manager.parserCache.Get(s.streamUrl); ok {
				s.logger.Debug().Msgf("directstream(http): Reusing prewarmed metadata parser for: %s", s.streamUrl)
				metadata := cached.GetMetadata(context.Background())
				if metadata != nil && metadata.Error == nil {
					playbackInfo.MkvMetadata = metadata
					playbackInfo.MkvMetadataParser = mo.Some(cached)
					s.playbackInfo = &playbackInfo
					return
				}
				// Cached parser was bad — fall through to a fresh parse.
			}

			reader, readErr := s.newMetadataReader()
			if readErr != nil {
				err = fmt.Errorf("failed to create reader for stream url: %w", readErr)
				s.logger.Error().Err(readErr).Msg("directstream(http): Failed to create reader for stream url")
				s.playbackInfoErr = err
				return
			}
			defer reader.Close() // Close this specific reader instance

			_, _ = reader.Seek(0, io.SeekStart)
			s.logger.Trace().Msgf("directstream(http): Loading metadata for stream url: %s", s.streamUrl)

			parser := mkvparser.NewMetadataParser(reader, s.logger)
			metadataCtx := s.manager.playbackCtx
			if metadataCtx == nil {
				metadataCtx = context.Background()
			}
			metadata := parser.GetMetadata(metadataCtx)
			if metadata.Error != nil {
				err = fmt.Errorf("failed to get metadata: %w", metadata.Error)
				s.logger.Error().Err(metadata.Error).Msg("directstream(http): Failed to get metadata")
				s.playbackInfoErr = err
				return
			}

			playbackInfo.MkvMetadata = metadata
			playbackInfo.MkvMetadataParser = mo.Some(parser)
			// Cache for instant re-press of this same episode (URL-keyed, short TTL).
			s.manager.parserCache.SetT(s.streamUrl, parser, 15*time.Minute)
		}

		s.playbackInfo = &playbackInfo
	})

	return s.playbackInfo, s.playbackInfoErr
}

// getStreamHandler is called by concrete types, passing themselves as the Stream interface
// so that subtitle streaming uses the correct outer stream.
func (s *httpBaseStream) getStreamHandler(outer Stream) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.streamUrl == "" {
			s.logger.Error().Msg("directstream(http): No URL to stream")
			http.Error(w, "No URL to stream", http.StatusNotFound)
			return
		}

		if r.Method == http.MethodHead {
			s.logger.Trace().Msg("directstream(http): Handling HEAD request")

			fileSize := s.contentLength
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
			w.Header().Set("Content-Type", s.LoadContentType())
			w.Header().Set("Accept-Ranges", "bytes")
			s.applyHeadRespHeaders(w.Header())
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHeader := r.Header.Get("Range")

		if err := s.initializeStream(); err != nil {
			s.logger.Error().Err(err).Msg("directstream(http): Failed to initialize FileStream")
			http.Error(w, "Failed to initialize FileStream", http.StatusInternalServerError)
			return
		}

		reader, err := s.getReader()
		if err != nil {
			s.logger.Error().Err(err).Msg("directstream(http): Failed to create reader for stream url")
			http.Error(w, "Failed to create reader for stream url", http.StatusInternalServerError)
			return
		}
		defer reader.Close()

		if isThumbnailRequest(r) {
			ra, ok := handleRange(w, r, reader, s.filename, s.contentLength)
			if !ok {
				return
			}
			serveContentRange(w, r, r.Context(), reader, s.filename, s.contentLength, s.contentType, ra)
			return
		}

		ra, ok := handleRange(w, r, reader, s.filename, s.contentLength)
		if !ok {
			return
		}

		if _, ok := s.playbackInfo.MkvMetadataParser.Get(); ok {
			subReader, err := s.getReader()
			if err != nil {
				s.logger.Error().Err(err).Msg("directstream(http): Failed to create subtitle reader for stream url")
				http.Error(w, "Failed to create subtitle reader for stream url", http.StatusInternalServerError)
				return
			}
			if ra.Start < s.contentLength-1024*1024 {
				// subReader is closed inside the subtitle goroutine
				go s.StartSubtitleStreamP(outer, s.manager.playbackCtx, subReader, ra.Start, 0)
			} else {
				_ = subReader.Close()
			}
		}

		// Fetch the CDN range, retrying transient throttling (429) / gateway errors with
		// capped backoff. The server re-pulls ranges as the user watches, so without this a
		// single momentary CDN rate-limit would fail the whole stream mid-episode.
		var resp *http.Response
		for attempt := 0; ; attempt++ {
			// Use the client's request context so the CDN request is cancelled when the client disconnects
			req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, s.streamUrl, nil)
			if reqErr != nil {
				http.Error(w, "Failed to create request", http.StatusInternalServerError)
				return
			}

			req.Header.Set("Accept", "*/*")
			req.Header.Set("Range", rangeHeader)

			// Only forward safe headers to avoid conflicts with the CDN
			for key, values := range r.Header {
				if proxyHopHeaders[http.CanonicalHeaderKey(key)] {
					continue
				}
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
			s.applyReqHeaders(req.Header)

			var doErr error
			resp, doErr = videoProxyClient.Do(req)

			// Success or a permanent non-2xx (handled just below) → stop retrying.
			if doErr == nil && !isCDNTransientStatus(resp.StatusCode) {
				break
			}

			status, retryAfter := 0, ""
			if doErr == nil {
				status = resp.StatusCode
				retryAfter = resp.Header.Get("Retry-After")
				resp.Body.Close()
			}

			// Give up if retries are exhausted or the client has disconnected.
			if attempt >= maxCDNRetries-1 || r.Context().Err() != nil {
				if doErr != nil {
					s.logger.Error().Err(doErr).Str("range", rangeHeader).Msg("directstream(http): CDN proxy request failed")
					http.Error(w, "Failed to proxy request", http.StatusBadGateway)
				} else {
					s.logger.Error().Int("status", status).Int("attempts", attempt+1).Str("range", rangeHeader).Msg("directstream(http): CDN throttled, retries exhausted")
					http.Error(w, fmt.Sprintf("CDN error: %d", status), status)
				}
				return
			}

			s.logger.Warn().Int("status", status).Int("attempt", attempt+1).Str("range", rangeHeader).Msg("directstream(http): CDN throttled, backing off")
			if !cdnRetryWait(r.Context(), attempt, retryAfter) {
				return // client disconnected during backoff
			}
		}
		defer resp.Body.Close()

		// Reject permanent non-2xx CDN responses to avoid corrupting the file cache
		if resp.StatusCode >= 300 {
			s.logger.Error().Int("status", resp.StatusCode).Str("range", rangeHeader).Msg("directstream(http): CDN returned non-2xx status")
			http.Error(w, fmt.Sprintf("CDN error: %d", resp.StatusCode), resp.StatusCode)
			return
		}

		// Copy response headers
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Set(key, value)
			}
		}

		w.Header().Set("Content-Type", s.LoadContentType()) // overwrite the type
		w.WriteHeader(resp.StatusCode)

		if err := s.httpStream.WriteAndFlush(resp.Body, w, ra.Start); err != nil {
			s.logger.Warn().Err(err).Str("range", rangeHeader).Msg("directstream(http): WriteAndFlush error")
		}
	})
}

// initializeStream creates the HTTP cache for this stream if it doesn't exist
func (s *httpBaseStream) initializeStream() error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if s.httpStream != nil {
		return nil // Already initialized
	}

	if s.streamUrl == "" {
		return fmt.Errorf("stream URL is not set")
	}

	// Get content length first
	if s.contentLength == 0 {
		info, ok := s.manager.FetchStreamInfoWithHeaders(s.streamUrl, s.requestHeaders)
		if !ok {
			return fmt.Errorf("failed to fetch stream info")
		}
		s.contentLength = info.ContentLength
	}

	s.logger.Debug().Msgf("directstream(http): Initializing FileStream for stream URL: %s", s.streamUrl)

	// Create a file-backed stream with the known content length
	cache, err := httputil.NewFileStream(s.manager.playbackCtx, s.logger, s.contentLength)
	if err != nil {
		return fmt.Errorf("failed to create FileStream: %w", err)
	}

	s.httpStream = cache

	s.logger.Debug().Msgf("directstream(http): FileStream initialized")

	return nil
}

func (s *httpBaseStream) getReader() (io.ReadSeekCloser, error) {
	if err := s.initializeStream(); err != nil {
		return nil, err
	}

	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	if s.httpStream == nil {
		return nil, fmt.Errorf("FileStream not initialized")
	}

	return s.httpStream.NewReader()
}
