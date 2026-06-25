package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	httputil "seanime/internal/util/http"
	"sync/atomic"
	"time"
)

// Read-ahead prefetch for HTTP/debrid directstreams.
//
// The directstream serves one CDN byte-range at a time, and the player (Denshi's chromium <video>)
// has only a small forward buffer with no real read-ahead — so any CDN dip drains it and stalls,
// and every seek used to cancel the in-flight CDN fetch ("connection reset by peer") and re-pull
// cold. This decouples the CDN fetch from the client request: a single fill goroutine pulls the CDN
// FORWARD into the FileStream cache, bounded by a window ahead of the player, surviving client
// seeks/disconnects; the client request is served from that cache (the reader blocks until pieces
// land). A CDN dip then drains the cached window instead of stalling, and a seek just repositions
// the fill instead of killing it. This is what gives Denshi the read-ahead MPV (Tenji) has natively.

const (
	// readAheadWindowBytes caps how far ahead of the player the prefetch may run — bounds disk + CDN
	// use. 96 MiB ≈ ~10s at a 80 Mbps remux, ~75s at 10 Mbps. ponytail: fixed byte cap, not adaptive
	// to measured bitrate; revisit if 4K remuxes want a deeper buffer.
	readAheadWindowBytes int64 = 96 << 20
	// maxFillReopens bounds consecutive CDN-reopen failures before the fill gives up (avoids a hot
	// loop on a permanently-dead link). Transient throttling is already retried inside openCDNRange.
	maxFillReopens = 8
	// fillStallTimeout: if the served request makes no progress this long (a wedged prefetch), the
	// reader is closed so the client re-requests and re-triggers the fill instead of hanging forever.
	fillStallTimeout = 20 * time.Second
)

// ensureFill makes sure the CDN->cache prefetch is covering `start` forward. No-op when the running
// fill already covers start (so the steady stream of range requests doesn't restart it); repositions
// the fill on a real seek to an uncovered offset.
func (s *httpBaseStream) ensureFill(start int64) {
	if start < 0 {
		start = 0
	}
	s.fillMu.Lock()
	defer s.fillMu.Unlock()

	// The running fill began at/before start and has reached it → it's already feeding this position.
	if s.fillActive && start >= s.fillFrom && start <= s.fillOff.Load() {
		s.serveOff.Store(start) // anchor the ahead-window to the live player position
		return
	}

	// Reposition: cancel the current fill and start a new one at `start`.
	if s.fillActive && s.fillCancel != nil {
		s.fillCancel()
	}
	ctx, cancel := context.WithCancel(s.manager.playbackCtx)
	s.fillCancel = cancel
	s.fillFrom = start
	s.fillOff.Store(start)
	s.serveOff.Store(start)
	s.fillActive = true
	go s.runFill(ctx, start)
}

// runFill pulls the CDN forward into the cache from `start`, skipping already-cached regions and
// pausing when it gets a window ahead of the player. It owns CDN retries: transient drops reopen
// from the current offset so a mid-stream blip doesn't end playback.
func (s *httpBaseStream) runFill(ctx context.Context, start int64) {
	defer func() {
		s.fillMu.Lock()
		if s.fillFrom == start { // only clear if a newer fill hasn't taken over
			s.fillActive = false
		}
		s.fillMu.Unlock()
	}()

	off := start
	buf := make([]byte, 32*1024)
	fails := 0

	for {
		if ctx.Err() != nil || (s.contentLength > 0 && off >= s.contentLength) {
			return
		}

		s.cacheMu.RLock()
		fs := s.httpStream
		s.cacheMu.RUnlock()
		if fs == nil {
			return
		}

		// Skip regions already downloaded (e.g. a backward seek into watched territory).
		if e := fs.ContiguousEnd(off); e >= off {
			off = e + 1
			s.fillOff.Store(off)
			continue
		}

		// Don't run too far ahead of the player.
		if off-s.serveOff.Load() > readAheadWindowBytes {
			if !sleepCtx(ctx, 50*time.Millisecond) {
				return
			}
			continue
		}

		resp, ok := s.openCDNRange(ctx, off)
		if !ok {
			fails++
			if fails >= maxFillReopens || ctx.Err() != nil {
				s.logger.Warn().Int64("offset", off).Msg("directstream(http): prefetch gave up after repeated CDN failures")
				return
			}
			if !sleepCtx(ctx, 500*time.Millisecond) {
				return
			}
			continue
		}
		fails = 0

		readErr := s.drainInto(ctx, fs, resp.Body, &off, buf)
		_ = resp.Body.Close()
		if readErr != nil && ctx.Err() == nil {
			// Mid-stream CDN drop → brief backoff, reopen from the current offset.
			if !sleepCtx(ctx, 300*time.Millisecond) {
				return
			}
		}
	}
}

// drainInto reads a CDN response body into the cache, advancing off, throttled to stay within the
// read-ahead window of the player.
func (s *httpBaseStream) drainInto(ctx context.Context, fs *httputil.FileStream, body io.Reader, off *int64, buf []byte) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Window throttle mid-response too (an open-ended CDN body must not outrun the player).
		if *off-s.serveOff.Load() > readAheadWindowBytes {
			if !sleepCtx(ctx, 50*time.Millisecond) {
				return ctx.Err()
			}
			continue
		}
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := fs.WriteCacheAt(buf[:n], *off); err != nil {
				return err
			}
			*off += int64(n)
			s.fillOff.Store(*off)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
		if s.contentLength > 0 && *off >= s.contentLength {
			return nil
		}
	}
}

// openCDNRange opens an open-ended CDN GET from `off`, retrying transient throttling (429) / gateway
// errors with capped backoff. Returns (resp, true) on a 2xx; (nil, false) on a permanent error or
// exhausted retries. Uses the stream's own request headers (auth) — not the per-client request.
func (s *httpBaseStream) openCDNRange(ctx context.Context, off int64) (*http.Response, bool) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.streamUrl, nil)
		if err != nil {
			return nil, false
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		s.applyReqHeaders(req.Header)

		resp, doErr := videoProxyClient.Do(req)
		if doErr == nil && resp.StatusCode < 300 {
			return resp, true
		}
		if doErr == nil && !isCDNTransientStatus(resp.StatusCode) {
			resp.Body.Close() // permanent (403/404/416) — don't retry
			return nil, false
		}

		retryAfter := ""
		if doErr == nil {
			retryAfter = resp.Header.Get("Retry-After")
			resp.Body.Close()
		}
		if attempt >= maxCDNRetries-1 || ctx.Err() != nil {
			return nil, false
		}
		if !cdnRetryWait(ctx, attempt, retryAfter) {
			return nil, false
		}
	}
}

// serveFromCache serves a byte range to the client from the cache the prefetcher is filling. The
// cache reader blocks until the needed pieces land, so the client is fed from the buffered-ahead
// window. serveOff is advanced as the player consumes, anchoring the prefetch window.
func (s *httpBaseStream) serveFromCache(w http.ResponseWriter, ctx context.Context, reader io.ReadSeekCloser, ra httputil.Range) {
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", s.LoadContentType())
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", "no-store")

	if ra.Start >= s.contentLength || ra.Start < 0 || ra.Length <= 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", s.contentLength))
		http.Error(w, "Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", ra.ContentRange(s.contentLength))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", ra.Length))
	w.WriteHeader(http.StatusPartialContent)

	if _, err := reader.Seek(ra.Start, io.SeekStart); err != nil {
		return
	}

	// Stall watchdog: if the prefetch wedges (permanent CDN failure mid-stream) the cache reader
	// would block forever. Close it after a no-progress timeout so the request ends and the player
	// re-requests (re-triggering the fill) instead of hanging.
	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastProgress.Load())) > fillStallTimeout {
					_ = reader.Close()
					return
				}
			}
		}
	}()

	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	var written int64
	for written < ra.Length {
		if ctx.Err() != nil {
			return
		}
		toRead := int64(len(buf))
		if ra.Length-written < toRead {
			toRead = ra.Length - written
		}
		n, rerr := reader.Read(buf[:toRead])
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			written += int64(n)
			s.serveOff.Store(ra.Start + written)
			lastProgress.Store(time.Now().UnixNano())
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// sleepCtx sleeps for d, returning false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
