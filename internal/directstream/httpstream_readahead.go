package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	httputil "seanime/internal/util/http"
	"strings"
	"sync"
	"time"
)

// Opt-in single-connection read-ahead for HTTP/debrid directstreams.
//
// The default serve path tees ONE CDN response straight to the client (WriteAndFlush), paced by the
// player's small buffer — so a CDN dip drains the player and stalls, with no read-ahead. This path
// keeps the SAME single CDN connection (the player's own range request, with its proven headers /
// retry) but splits it: a producer reads the response into the FileStream cache AHEAD of the player,
// and the client is served from that cache. The cache then rides CDN dips. Crucially it adds NO
// extra CDN connection (the earlier separate-prefetcher attempt diverged from the working request
// path and failed), and it can't corrupt offsets (gated to 206 responses) or starve the player
// (an incomplete fill closes the reader so the request ends and the player re-requests, exactly like
// the live-tee path on a CDN drop). Default OFF; flip SEANIME_DIRECTSTREAM_READAHEAD=1 to enable.

// readAheadWindowBytes caps how far ahead of the player the producer fills — bounds disk + lets a
// CDN dip be absorbed. 96 MiB ≈ ~10s at 80 Mbps, ~75s at 10 Mbps.
const readAheadWindowBytes int64 = 96 << 20

var (
	readAheadOnce sync.Once
	readAheadOn   bool
)

// readAheadEnabled reports whether the opt-in read-ahead path is on (env, read once).
func readAheadEnabled() bool {
	readAheadOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv("SEANIME_DIRECTSTREAM_READAHEAD"))
		readAheadOn = v == "1" || strings.EqualFold(v, "true")
	})
	return readAheadOn
}

// serveReadAhead serves one range with read-ahead: a producer fills the cache from `resp` ahead of
// the player, the consumer serves the client from the cache. Single connection (this `resp`).
func (s *httpBaseStream) serveReadAhead(w http.ResponseWriter, r *http.Request, reader io.ReadSeekCloser, resp *http.Response, ra httputil.Range, rangeHeader string) {
	ctx := r.Context()
	s.serveOff.Store(ra.Start)
	s.fillOff.Store(ra.Start)

	go s.fillFromBody(ctx, resp.Body, reader, ra, rangeHeader)
	s.serveFromCacheBuffered(w, ctx, reader, ra)
}

// fillFromBody reads the CDN response body into the cache from ra.Start, paused when it gets a window
// ahead of the player. On an incomplete stop (CDN drop / cancel) it closes the reader so the consumer
// doesn't block forever; on a full fill of the requested span the consumer finishes on its own.
func (s *httpBaseStream) fillFromBody(ctx context.Context, body io.Reader, reader io.Closer, ra httputil.Range, rangeHeader string) {
	s.cacheMu.RLock()
	fs := s.httpStream
	s.cacheMu.RUnlock()
	if fs == nil {
		_ = reader.Close()
		return
	}

	end := ra.Start + ra.Length // exclusive: end of the requested span
	off := ra.Start
	buf := make([]byte, 64*1024)

	for off < end {
		if ctx.Err() != nil {
			break
		}
		// Don't fill more than a window ahead of what the player has consumed.
		if off-s.serveOff.Load() > readAheadWindowBytes {
			if !sleepCtx(ctx, 50*time.Millisecond) {
				break
			}
			continue
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if err := fs.WriteCacheAt(buf[:n], off); err != nil {
				break
			}
			off += int64(n)
			s.fillOff.Store(off)
		}
		if rerr != nil {
			if rerr != io.EOF {
				s.logger.Warn().Err(rerr).Int64("offset", off).Str("range", rangeHeader).Msg("directstream(http): read-ahead producer CDN read error")
			}
			break
		}
	}

	if off < end {
		// Stopped short → unblock a waiting consumer so the request ends and the player re-requests.
		_ = reader.Close()
	}
}

// serveFromCacheBuffered serves ra to the client from the cache reader (blocks until the producer
// fills each piece), advancing serveOff so the producer's window tracks the player.
func (s *httpBaseStream) serveFromCacheBuffered(w http.ResponseWriter, ctx context.Context, reader io.ReadSeekCloser, ra httputil.Range) {
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", s.LoadContentType())
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Range", ra.ContentRange(s.contentLength))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", ra.Length))
	w.WriteHeader(http.StatusPartialContent)

	if _, err := reader.Seek(ra.Start, io.SeekStart); err != nil {
		return
	}

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
