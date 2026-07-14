package directstream

import (
	"fmt"
	"io"
	"net/http"
	"seanime/internal/util"
	httputil "seanime/internal/util/http"
	"time"
)

// mpv's open of a remote MKV issues several SEQUENTIAL range requests: EBML header +
// tracks + attachment fonts at the head (fonts run 5-40MiB on dual-audio remuxes), then
// SeekHead → Cues at the tail, then initial clusters. Against a cold debrid CDN each
// request pays high first-byte latency, which is the dominant multi-second chunk of
// MpvCore's "Starting video...". These windows are warmed into the shared FileStream
// cache the moment the stream binds, and the handler serves any cached prefix from disk.
const (
	warmHeadBytes = int64(24 << 20) // EBML header + tracks + fonts
	warmTailBytes = int64(4 << 20)  // SeekHead → Cues
)

// mpvCoreProxied reports whether this stream hands MpvCore the proxy URL even in
// direct-CDN mode, so the probe windows come from the warmed cache. VideoCore direct
// mode is untouched.
func (s *httpBaseStream) mpvCoreProxied() bool {
	return s.directMode() && s.playbackTarget == PlaybackTargetMpvCore
}

// cdnHandoff: in direct mode the proxy serves ONLY the warm windows itself; every range
// outside them is answered with a 302 to the raw CDN link. ffmpeg adopts the redirect
// target for subsequent requests, so after the first handoff the server is out of the
// data path entirely — warm-cache startup AND direct-CDN egress. The main sequential
// read is moved over by capping the head-window response at the window edge: mpv's
// standard reconnect logic re-requests at that offset and gets redirected.
// Fallback if a client ever fails to reconnect: turn the Direct CDN toggle off →
// mpvCoreProxied is false → plain full-proxy serving.
func (s *httpBaseStream) cdnHandoff() bool {
	return s.mpvCoreProxied() && s.clientStreamUrl != ""
}

// handoffWindow returns the exclusive end of the warm window containing offset, or 0 if
// the offset is outside both windows. Small files (fully covered by the windows) always
// resolve to contentLength, so they never redirect and never truncate.
func (s *httpBaseStream) handoffWindow(offset int64) int64 {
	head := warmHeadBytes
	tailStart := s.contentLength - warmTailBytes
	if s.contentLength <= warmHeadBytes+warmTailBytes {
		return s.contentLength
	}
	if offset < head {
		return head
	}
	if offset >= tailStart {
		return s.contentLength
	}
	return 0
}

// serveHandoff answers a range request in handoff mode: redirect out-of-window ranges to
// the CDN; serve in-window ranges from the warm cache (waiting on the in-flight warm),
// capped at the window edge so the client's reconnect lands outside and gets redirected.
func (s *httpBaseStream) serveHandoff(w http.ResponseWriter, r *http.Request, ra httputil.Range) {
	windowEnd := s.handoffWindow(ra.Start)
	if windowEnd == 0 {
		s.logger.Debug().Int64("start", ra.Start).Msg("directstream(http): Handoff 302 to CDN (out of window)")
		http.Redirect(w, r, s.clientStreamUrl, http.StatusFound)
		return
	}

	warmFailed := &s.warmHeadFailed
	if ra.Start >= s.contentLength-warmTailBytes && s.contentLength > warmHeadBytes+warmTailBytes {
		warmFailed = &s.warmTailFailed
	}
	// Dead warm → hand the whole range off to the CDN. Redirect whenever the warm failed, NOT
	// only when nothing is cached: a PARTIAL fill (0 < cached span < the 1 MiB serve chunk) can
	// no longer complete, so entering the serve loop below would answer with a zero-payload 206
	// and the player would reconnect at the same offset forever. The CDN serves the full range.
	if warmFailed.Load() {
		s.logger.Debug().Int64("start", ra.Start).Msg("directstream(http): Handoff 302 to CDN (warm dead)")
		http.Redirect(w, r, s.clientStreamUrl, http.StatusFound)
		return
	}
	s.logger.Debug().Int64("start", ra.Start).Int64("windowEnd", windowEnd).Msg("directstream(http): Handoff serving warm window")

	// Advertise the FULL requested range; delivering only the window and returning early
	// closes the connection, which the player treats as a transient drop and re-requests
	// from where it left off (landing in the redirect branch above).
	total := ra.Length
	end := ra.Start + total - 1
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", s.LoadContentType())
	w.Header().Set("Content-Length", fmt.Sprintf("%d", total))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", ra.Start, end, s.contentLength))
	w.WriteHeader(http.StatusPartialContent)

	reader, err := s.getReader()
	if err != nil {
		return
	}
	defer reader.Close()
	if _, err := reader.Seek(ra.Start, io.SeekStart); err != nil {
		return
	}

	// Stream the window in chunks, waiting for the in-flight warm to fill each one. If
	// the warm failed, stop — the client reconnects and the CDN serves the rest.
	serveEnd := windowEnd
	if end+1 < serveEnd {
		serveEnd = end + 1 // bounded request entirely inside the window
	}
	flusher, _ := w.(http.Flusher)
	pbCtx := s.manager.PlaybackCtx() // snapshot under lock (release path nils it concurrently)
	off := ra.Start
	for off < serveEnd {
		chunk := int64(1 << 20)
		if off+chunk > serveEnd {
			chunk = serveEnd - off
		}
		for !s.httpStream.IsRangeAvailable(off, off+chunk-1) {
			if warmFailed.Load() || r.Context().Err() != nil ||
				(pbCtx != nil && pbCtx.Err() != nil) {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		if _, err := io.CopyN(w, reader, chunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		off += chunk
	}
	// If the request extended past the window, returning here truncates the response at
	// the window edge on purpose (see comment above).
}

// warmProbeWindows fills the head/tail probe windows in the background. Called once per
// stream (warmOnce); no-ops if the cache can't initialize.
func (s *httpBaseStream) warmProbeWindows() {
	if err := s.initializeStream(); err != nil {
		s.logger.Warn().Err(err).Msg("directstream(http): Probe-window warm skipped, cache init failed")
		s.warmHeadFailed.Store(true)
		s.warmTailFailed.Store(true)
		return
	}
	if s.contentLength <= 0 {
		s.warmHeadFailed.Store(true)
		s.warmTailFailed.Store(true)
		return
	}
	head := warmHeadBytes
	if head > s.contentLength {
		head = s.contentLength
	}
	go s.warmRange(0, head)
	if tailStart := s.contentLength - warmTailBytes; tailStart > head {
		go s.warmRange(tailStart, warmTailBytes)
	}
}

// warmRange downloads [start, start+length) into the FileStream cache (no client tee).
// Uses the per-token gate + transient-retry helpers so it can't hammer the link.
func (s *httpBaseStream) warmRange(start, length int64) {
	defer util.HandlePanicInModuleThen("directstream/warmRange", func() {})

	ctx := s.manager.playbackCtx
	if ctx == nil {
		return
	}
	end := start + length - 1
	if s.httpStream.IsRangeAvailable(start, end) {
		return
	}

	// Bytes captured at prewarm time (RAM) beat a CDN round-trip on the startup path.
	if data := prewarmedWindowBytes(s.streamUrl, start, length); data != nil {
		if s.httpStream.WriteCacheAt(data, start) == nil {
			s.logger.Debug().Int64("start", start).Int64("bytes", int64(len(data))).Msg("directstream(http): Probe window filled from prewarm capture")
			return
		}
	}

	release, err := cdnTokenGateInst.acquire(ctx, cdnTokenKey(s.streamUrl))
	if err != nil {
		return
	}
	defer release()

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, s.streamUrl, nil)
		if reqErr != nil {
			return
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		s.applyReqHeaders(req.Header)

		var doErr error
		resp, doErr = videoProxyClient.Do(req)
		if doErr == nil && !isCDNTransientStatus(resp.StatusCode) {
			break
		}
		status, retryAfter := 0, ""
		if doErr == nil {
			status = resp.StatusCode
			retryAfter = resp.Header.Get("Retry-After")
			resp.Body.Close()
		}
		if attempt >= maxCDNRetries-1 || ctx.Err() != nil {
			s.logger.Warn().Int("status", status).Int64("start", start).Msg("directstream(http): Probe-window warm failed")
			s.markWarmFailed(start)
			return
		}
		if !cdnRetryWait(ctx, attempt, retryAfter) {
			s.markWarmFailed(start)
			return
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		s.markWarmFailed(start)
		return
	}

	buf := make([]byte, 256*1024)
	off := start
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if s.httpStream.WriteCacheAt(buf[:n], off) != nil {
				s.markWarmFailed(start)
				return
			}
			off += int64(n)
		}
		if readErr != nil {
			if readErr != io.EOF || off < end+1 {
				s.markWarmFailed(start)
			}
			break
		}
	}
	s.logger.Debug().Int64("start", start).Int64("bytes", off-start).Msg("directstream(http): Probe window warmed")
}

// markWarmFailed flags the window whose warm gave up, so a capped serve waiting on it
// stops and lets the client fall through to the CDN.
func (s *httpBaseStream) markWarmFailed(start int64) {
	if start == 0 {
		s.warmHeadFailed.Store(true)
	} else {
		s.warmTailFailed.Store(true)
	}
}

// serveStitched serves the cached prefix of the requested range straight from disk, then
// continues the remainder with a live CDN tee (which fills the cache like the normal
// path). Transparent to the player: one 206 response for the exact requested range.
func (s *httpBaseStream) serveStitched(w http.ResponseWriter, r *http.Request, ra httputil.Range, span int64) {
	total := ra.Length
	if span > total {
		span = total
	}
	end := ra.Start + total - 1

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", s.LoadContentType())
	w.Header().Set("Content-Length", fmt.Sprintf("%d", total))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", ra.Start, end, s.contentLength))
	w.WriteHeader(http.StatusPartialContent)

	reader, err := s.getReader()
	if err != nil {
		return
	}
	defer reader.Close()
	if _, err := reader.Seek(ra.Start, io.SeekStart); err != nil {
		return
	}
	if _, err := io.CopyN(w, reader, span); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if span >= total {
		return
	}

	// Remainder: live CDN tee from where the cache ran out.
	remStart := ra.Start + span
	release, gateErr := cdnTokenGateInst.acquire(r.Context(), cdnTokenKey(s.streamUrl))
	if gateErr != nil {
		return
	}
	defer release()

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, s.streamUrl, nil)
		if reqErr != nil {
			return
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", remStart, end))
		s.applyReqHeaders(req.Header)

		var doErr error
		resp, doErr = videoProxyClient.Do(req)
		if doErr == nil && !isCDNTransientStatus(resp.StatusCode) {
			break
		}
		status, retryAfter := 0, ""
		if doErr == nil {
			status = resp.StatusCode
			retryAfter = resp.Header.Get("Retry-After")
			resp.Body.Close()
		}
		if attempt >= maxCDNRetries-1 || r.Context().Err() != nil {
			s.logger.Warn().Int("status", status).Msg("directstream(http): Stitched remainder fetch failed")
			return
		}
		if !cdnRetryWait(r.Context(), attempt, retryAfter) {
			return
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		s.logger.Warn().Int("status", resp.StatusCode).Msg("directstream(http): Stitched remainder got non-2xx")
		return
	}

	if err := s.httpStream.WriteAndFlush(resp.Body, w, remStart); err != nil {
		if isBenignStreamWriteErr(err) {
			// Client went away mid-write (seek / close / buffer-full) — normal for seekable video.
			s.logger.Trace().Err(err).Msg("directstream(http): client disconnected during stitched write")
		} else {
			s.logger.Warn().Err(err).Msg("directstream(http): Stitched WriteAndFlush error")
		}
	}
}
