package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"seanime/internal/library/anime"
	"seanime/internal/mkvparser"
	"seanime/internal/player"
	httputil "seanime/internal/util/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/samber/mo"
)

// httpBaseStream holds shared state and logic for HTTP URL-based streams (debrid, URL, nakama).
type httpBaseStream struct {
	BaseStream
	streamUrl string
	// clientStreamUrl, when non-empty, puts the stream in direct CDN mode: PlaybackInfo hands
	// this raw URL to the player (no proxy), and subtitle readers pull from streamUrl (the
	// server's own link) via chunked CDN readers instead of the proxy-fed FileStream. The
	// proxy endpoint stays alive for thumbnails / non-capable consumers.
	clientStreamUrl string
	// cdnGated marks a stream whose streamUrl is a debrid CDN link: metadata/subtitle reads go
	// through the per-token gate + chunked reader (see cdngate.go — TorBox throttles per-link).
	// Other http streams (Nakama peers, plain URLs) are not per-link throttled, so they use the
	// plain lazy reader and issue byte-exact ranges.
	cdnGated bool
	// expectedSize is the size the debrid provider reports for this file (0 = unknown). The CDN's
	// Content-Length is checked against it in loadPlaybackInfo.
	expectedSize        int64
	contentLength       int64
	filepath            string
	requestHeaders      http.Header
	headResponseHeaders http.Header
	httpStream          *httputil.FileStream // Shared file-backed cache for multiple readers
	cacheMu             sync.RWMutex         // Protects httpStream access

	// Read-ahead window bookkeeping (opt-in path, see httpstream_readahead.go). fillOff = how far the
	// producer has written into the cache; serveOff = how far the player has consumed. The producer
	// pauses when it gets readAheadWindowBytes ahead of serveOff. Per-stream (one active main request
	// at a time); reset at the start of each buffered serve.
	fillOff  atomic.Int64
	serveOff atomic.Int64

	// warmOnce guards the MpvCore probe-window warm (httpstream_warm.go).
	warmOnce sync.Once
	// warmHeadFailed/warmTailFailed let the capped window serve stop waiting for a warm
	// that will never complete (client then reconnects and gets redirected to the CDN).
	warmHeadFailed atomic.Bool
	warmTailFailed atomic.Bool
}

var videoProxyClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		// Bound concurrent connections to a single CDN host so a burst of range requests
		// (stream start + metadata + a seek) can't hammer the debrid CDN into 429ing the
		// token. Excess requests queue rather than fail. This is a PROCESS-WIDE cap shared by
		// every user, and a proxy serve holds a connection for a range's whole live-tee
		// duration, so 8 serialized concurrent viewers on a multi-user server. The per-link
		// cdngate (cap 2/link) already prevents any single link from flooding the CDN, so raise
		// the cross-user ceiling to 16 to avoid queueing legit concurrent streams.
		MaxConnsPerHost:     16,
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

// truncatedStreamErr reports a CDN serving fewer bytes than the provider says the file has.
// served/expected <= 0 means "no opinion" (unknown size, or the length wasn't fetched) and never
// trips, so this is inert for providers that can't report a size.
func truncatedStreamErr(served, expected int64) error {
	if expected <= 0 || served <= 0 || served == expected {
		return nil
	}
	return fmt.Errorf("debrid CDN served a truncated file: %d of %d bytes (%.2f%%) — the torrent's cached copy is incomplete on the provider; re-add it or pick another release",
		served, expected, float64(served)/float64(expected)*100)
}

func (s *httpBaseStream) newMetadataReader() (io.ReadSeekCloser, error) {
	if !s.cdnGated {
		return httputil.NewLazyHttpReadSeekerFromURLWithHeaders(s.streamUrl, s.requestHeaders)
	}
	return fetchMetadataReader(s.manager.playbackCtx, s.logger, s.streamUrl, s.requestHeaders)
}

// directMode reports whether the client plays the CDN URL itself (no proxy).
func (s *httpBaseStream) directMode() bool {
	return s.clientStreamUrl != ""
}

// directSubtitleWalkBytesPerSec paces the direct-mode subtitle cluster walk. The client's
// <video> pulls video from the SAME CDN link (TorBox mints one URL per file), so an unpaced
// walk at line speed competes with playback and trips the link's throttle (429s). 6 MiB/s
// stays far ahead of any realistic bitrate (~1 MiB/s) while cutting the walk's burst 10x+.
// ponytail: fixed rate, not playback-position-aware; tie the walk to player progress if a
// very high-bitrate remux ever outruns it.
const directSubtitleWalkBytesPerSec = 6 << 20

// newSubtitleReader returns the reader subtitle streams should walk the file with.
// Proxy mode: a FileStream reader (fed by the proxy's CDN pulls). Direct mode: the proxy
// never fills the cache, so read the server link directly — same gated chunked CDN reader
// as the metadata parse (per-token slot, transient-429 retry), throughput-paced so the walk
// doesn't compete with the client's video pulls on the shared link.
func (s *httpBaseStream) newSubtitleReader() (io.ReadSeekCloser, error) {
	if s.directMode() {
		reader, err := s.newMetadataReader()
		if err != nil {
			return nil, err
		}
		return newPacedReadSeekCloser(reader, directSubtitleWalkBytesPerSec), nil
	}
	return s.getReader()
}

// pacedReadSeekCloser throttles cumulative read throughput to rate bytes/sec by sleeping off
// the overshoot after each read. Seeks are free (no bytes transferred for the skipped span).
type pacedReadSeekCloser struct {
	io.ReadSeekCloser
	rate  float64
	start time.Time
	read  int64
}

func newPacedReadSeekCloser(r io.ReadSeekCloser, bytesPerSec int64) *pacedReadSeekCloser {
	return &pacedReadSeekCloser{ReadSeekCloser: r, rate: float64(bytesPerSec), start: time.Now()}
}

func (p *pacedReadSeekCloser) Read(b []byte) (int, error) {
	n, err := p.ReadSeekCloser.Read(b)
	p.read += int64(n)
	ahead := time.Duration(float64(p.read)/p.rate*float64(time.Second)) - time.Since(p.start)
	if ahead > 0 {
		// Cap each pause so teardown (reader Close between reads) stays responsive.
		time.Sleep(min(ahead, time.Second))
	}
	return n, err
}

// fetchMetadataReader opens a CDN reader for MKV metadata.
//
// Chunked + cached: the parser seeks through header → tracks → attachments → cues-at-tail, and a
// plain reader opened a NEW range request per seek (~5-15 rapid GETs on one link — the burst that
// trips the CDN's per-link throttling, surfacing as 0-track parses). The chunked reader fetches
// 8MiB-aligned ranges, caches them, and retries transient 429s internally, collapsing the parse to
// a handful of spaced requests.
//
// The reader holds a per-token gate slot for its whole lifetime (released on Close) so the metadata
// read counts against the same per-link connection budget as the serve path — a metadata parse and
// the player's head fetch can't both burst the same TorBox token.
func fetchMetadataReader(ctx context.Context, logger *zerolog.Logger, url string, headers http.Header) (io.ReadSeekCloser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := cdnTokenGateInst.acquire(ctx, cdnTokenKey(url))
	if err != nil {
		return nil, err // context cancelled while queued for a slot
	}
	// No I/O here — the chunked reader fetches lazily (with its own transient-429 retry), so the
	// gate slot is held only while the parse actually reads.
	reader := httputil.NewChunkedHttpReadSeeker(url, headers)
	return &gatedReadSeekCloser{ReadSeekCloser: reader, release: release}, nil
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

// loadPlaybackInfo is called by concrete types, passing their own PlaybackType.
func (s *httpBaseStream) loadPlaybackInfo(streamType player.PlaybackType) (ret *player.PlaybackInfo, err error) {
	s.playbackInfoOnce.Do(func() {
		if s.streamUrl == "" {
			ret = &player.PlaybackInfo{}
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

		// A dead/expired debrid link (or an unreachable nakama host) can't produce a streamable
		// content type: FetchStreamInfo rejects the text/plain (or html/json) error body a CDN
		// returns for a 404/expired token, so LoadContentType comes back empty. VideoCore already
		// aborts on this in loadStream; MpvCore (Denshi) skips that gate and would otherwise open the
		// player on the error page and buffer FOREVER. Fail the open here so the client gets an error
		// and recovers — a watch-room follower's auto-follow / "Join room stream" re-resolves a fresh
		// link (nakama-room-sync treats a server abort as "not a user close"), instead of hanging.
		if contentType == "" {
			err = fmt.Errorf("stream url returned no streamable content type (link likely dead or expired)")
			s.logger.Error().Str("url", s.streamUrl).Msg("directstream(http): No streamable content type; aborting open (link likely dead/expired)")
			s.playbackInfoErr = err
			return
		}

		// A CDN can answer 200 with a TRUNCATED file: TorBox served 12,360,092 bytes of a
		// 1,427,264,036-byte episode (0.87% ≈ 12s) for a torrent whose own API entry still said
		// progress=1/cached=true — valid mkv magic, so the content-type gate above passes and the
		// container's header still declares the full duration. That plays a few seconds and then
		// silently refuses to seek (there is nothing past it). contentLength is already fetched by
		// LoadContentType, so this costs nothing. Only trips when the provider told us the real
		// size (expectedSize > 0); re-resolving does NOT help (the same torrent hands back the same
		// rotten link), so fail loudly and name the truncation instead of playing a fragment.
		if truncErr := truncatedStreamErr(s.contentLength, s.expectedSize); truncErr != nil {
			err = truncErr
			s.logger.Error().
				Str("url", s.streamUrl).
				Int64("served", s.contentLength).
				Int64("expected", s.expectedSize).
				Msg("directstream(http): CDN Content-Length disagrees with the provider's file size; aborting open (truncated/rotten cached copy)")
			s.playbackInfoErr = err
			return
		}

		// Direct CDN mode: the player pulls straight from the debrid CDN (no {{SERVER_URL}}
		// template, no HMAC — the CDN URL carries its own token). Proxy URL otherwise.
		// Exception: MpvCore stays on the proxy even in direct mode — its MKV probe
		// (header + fonts + Cues) is served from the warmed cache (see httpstream_warm.go).
		streamURL := "{{SERVER_URL}}/api/v1/directstream/stream?id=" + id + s.manager.GetHMACTokenQueryParam("/api/v1/directstream/stream", "&")
		if s.directMode() && !s.mpvCoreProxied() {
			streamURL = s.clientStreamUrl
		}

		// Warm the probe windows ahead of the player's first request.
		if s.playbackTarget == PlaybackTargetMpvCore {
			s.warmOnce.Do(s.warmProbeWindows)
		}

		playbackInfo := player.PlaybackInfo{
			ID:                id,
			PlaybackType:      streamType,
			PlaybackURI:       streamURL,
			StreamPath:        s.filepath,
			MimeType:          contentType,
			StreamURL:         streamURL,
			ContentLength:     s.contentLength, // loaded by LoadContentType
			MkvMetadata:       nil,
			MkvMetadataParser: mo.None[*mkvparser.MetadataParser](),
			Episode:           s.episode,
			Media:             s.media,
			EntryListData:     entryListData,
		}

		// VideoCore needs server-side MKV metadata and subtitle extraction.
		// MpvCore only needs the byte proxy; libmpv probes and demuxes the stream.
		if s.shouldProcessMediaOnServer() &&
			(isEbmlContent(s.LoadContentType()) || s.LoadContentType() == "application/octet-stream" || s.LoadContentType() == "application/force-download") {
			// Reuse a prewarmed parser for this URL if one exists — its GetMetadata is sync.Once
			// cached, so this skips the ~2-3s parse (font download) entirely. Subtitle/attachment
			// serving creates its own readers, so the parser's closed original reader is irrelevant.
			if cached, ok := s.manager.parserCache.Get(s.streamUrl); ok {
				s.logger.Debug().Msgf("directstream(http): Reusing prewarmed metadata parser for: %s", s.streamUrl)
				metadata := cached.GetMetadata(context.Background())
				if metadata != nil && metadata.Error == nil && len(metadata.Tracks) > 0 {
					playbackInfo.MkvMetadata = metadata
					playbackInfo.MkvMetadataParser = mo.Some(cached)
					s.playbackInfo = &playbackInfo
					return
				}
				// Cached parser was bad (errored or track-less) — fall through to a fresh parse.
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
			baseCtx := s.manager.playbackCtx
			if baseCtx == nil {
				baseCtx = context.Background()
			}
			// Bound the parse: a CDN that STALLS mid-body (not erroring) would otherwise hang
			// GetMetadata forever — "watch" never fires and the player sits on "Loading
			// metadata…" indefinitely. Generous bound: a normal parse (incl. font downloads
			// over the chunked reader) finishes in a few seconds.
			metadataCtx, cancelMetadata := context.WithTimeout(baseCtx, metadataParseTimeout)
			metadata := parser.GetMetadata(metadataCtx)
			cancelMetadata()
			if metadata.Error != nil {
				err = fmt.Errorf("failed to get metadata: %w", metadata.Error)
				s.logger.Error().Err(metadata.Error).Msg("directstream(http): Failed to get metadata")
				s.playbackInfoErr = err
				return
			}
			// 0 tracks usually means a transient CDN throttle (429) mid-parse, NOT a bad file.
			// Don't block playback — the player decodes the raw container itself; only server-side
			// subtitle/track features degrade. Just skip caching so a poisoned (track-less) parser
			// isn't reused for the 2h TTL. (ponytail: degrade, don't hard-fail; the real fix is
			// retrying 429s on the metadata read path so the parse completes.)
			if len(metadata.Tracks) == 0 {
				s.logger.Warn().Str("url", s.streamUrl).Msg("directstream(http): Metadata parse produced 0 tracks (likely CDN throttle); playing without server-parsed tracks")
				playbackInfo.MkvMetadata = metadata
				playbackInfo.MkvMetadataParser = mo.Some(parser)
				s.playbackInfo = &playbackInfo
				return
			}

			playbackInfo.MkvMetadata = metadata
			playbackInfo.MkvMetadataParser = mo.Some(parser)
			// Cache for instant re-press of this same episode (URL-keyed, short TTL).
			s.manager.parserCache.SetT(s.streamUrl, parser, metadataCacheTTL)
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
				// subReader is closed inside the subtitle goroutine. Snapshot playbackCtx under
				// lock (racing the release path that nils it) and recover: this is a detached
				// goroutine, so an unrecovered panic here would take down the whole server.
				pbCtx := s.manager.PlaybackCtx()
				go func() {
					defer func() {
						if r := recover(); r != nil {
							s.logger.Error().Interface("panic", r).Msg("directstream(http): recovered panic in subtitle stream goroutine")
							_ = subReader.Close()
						}
					}()
					s.StartSubtitleStreamP(outer, pbCtx, subReader, ra.Start, 0)
				}()
			} else {
				_ = subReader.Close()
			}
		}

		// MpvCore in direct mode: serve only the warm probe windows from the server and
		// 302 everything else to the raw CDN — the player adopts the redirect target, so
		// after the first handoff the server is out of the video data path (cdnHandoff).
		// In plain proxy mode: serve any cached prefix from disk, live-tee the remainder.
		if s.playbackTarget == PlaybackTargetMpvCore {
			if s.cdnHandoff() {
				s.serveHandoff(w, r, ra)
				return
			}
			if span := s.httpStream.CachedSpanFrom(ra.Start); span > 0 {
				s.serveStitched(w, r, ra, span)
				return
			}
		}

		// Bound simultaneous CDN connections to this single debrid link (token): the player's
		// head (bytes=0-) + tail-index probe + any seek all hit the same token at stream-start, and
		// that burst is what trips TorBox's per-link 429. Per-token, so other streams/users are
		// unaffected. Held until this range finishes serving (defer).
		releaseSlot, gateErr := cdnTokenGateInst.acquire(r.Context(), cdnTokenKey(s.streamUrl))
		if gateErr != nil {
			return // client disconnected while queued for a slot
		}
		defer releaseSlot()

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
					s.logger.Error().Str("origin", "cdn:"+cdnHost(s.streamUrl)).Int("status", status).Int("attempts", attempt+1).Str("range", rangeHeader).Msg("directstream(http): CDN throttled, retries exhausted")
					http.Error(w, fmt.Sprintf("CDN error: %d", status), status)
				}
				return
			}

			s.logger.Warn().Str("origin", "cdn:"+cdnHost(s.streamUrl)).Int("status", status).Int("attempt", attempt+1).Str("range", rangeHeader).Msg("directstream(http): CDN throttled, backing off")
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

		// Opt-in read-ahead (SEANIME_DIRECTSTREAM_READAHEAD=1): serve from the cache while a producer
		// fills it ahead of the player from THIS same CDN response — one connection, the cache rides
		// CDN dips. Default OFF → the proven live-tee path below, byte-for-byte unchanged.
		if readAheadEnabled() && resp.StatusCode == http.StatusPartialContent {
			s.serveReadAhead(w, r, reader, resp, ra, rangeHeader)
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
			if isBenignStreamWriteErr(err) {
				// Client went away mid-write (seek / close / buffer-full) — normal for seekable video.
				s.logger.Trace().Err(err).Str("range", rangeHeader).Msg("directstream(http): client disconnected during write")
			} else {
				s.logger.Warn().Err(err).Str("range", rangeHeader).Msg("directstream(http): WriteAndFlush error")
			}
		}
	})
}

// isBenignStreamWriteErr reports whether a WriteAndFlush error is just the client going away
// (seek, close, buffer-full) rather than a real server-side fault. These are routine for
// seekable HTTP video — the player constantly opens range requests and abandons them — so they
// must not be logged at WARN.
func isBenignStreamWriteErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"context canceled",
		"broken pipe",
		"connection reset",
		"reset by peer",
		"PROTOCOL_ERROR",
		"use of closed network connection",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
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
