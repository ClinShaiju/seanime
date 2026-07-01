package httputil

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChunkedHttpReadSeeker is an io.ReadSeekCloser over an HTTP URL that fetches in fixed-size,
// chunk-aligned range requests and caches recent chunks in memory.
//
// Why: the MKV metadata parse seeks through header → tracks → attachments → cues-at-tail, and the
// plain HttpReadSeeker opens a BRAND-NEW range request on every seek — one "Loading metadata" step
// was ~5-15 rapid GETs on the same debrid link, which is exactly the burst that trips the CDN's
// per-link throttling. Chunk-aligned fetches collapse that to a handful (header chunk + attachment
// chunks + tail chunk), and revisited regions are served from memory with zero requests.
//
// Concurrency: NOT safe for concurrent use (same contract as HttpReadSeeker — one parser owns it).
type ChunkedHttpReadSeeker struct {
	url     string
	headers http.Header
	client  *http.Client

	chunkSize int64
	maxChunks int // bounded memory: chunkSize × maxChunks

	mu     sync.Mutex
	offset int64
	size   int64 // -1 until learned from the first response
	chunks map[int64][]byte
	order  []int64 // insertion order for eviction (metadata access is ~sequential per region)
	closed bool
}

const (
	// chunkedDefaultSize balances request count vs wasted bytes: big enough that a 20-30MB font
	// attachment region is 3-4 requests (not dozens), small enough that the header/cues probes
	// don't pull tens of MB. The CDN warm already pulls 16-48MB per play, so this is in-family.
	chunkedDefaultSize int64 = 8 << 20
	// chunkedDefaultMaxChunks bounds the cache at chunkSize × N (48MiB). The parse pattern is
	// sequential within a region, so evicting the oldest chunk almost never causes a refetch.
	chunkedDefaultMaxChunks = 6
)

// NewChunkedHttpReadSeeker builds the reader. No I/O happens until the first Read/Seek-end —
// callers holding a connection-gate slot pay for requests only when the parse actually reads.
func NewChunkedHttpReadSeeker(url string, headers http.Header) *ChunkedHttpReadSeeker {
	return &ChunkedHttpReadSeeker{
		url:       url,
		headers:   headers,
		client:    http.DefaultClient,
		chunkSize: chunkedDefaultSize,
		maxChunks: chunkedDefaultMaxChunks,
		size:      -1,
		chunks:    make(map[int64][]byte),
	}
}

func (c *ChunkedHttpReadSeeker) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if c.size >= 0 && c.offset >= c.size {
		return 0, io.EOF
	}

	chunkStart := c.offset - (c.offset % c.chunkSize)
	chunk, err := c.chunkAt(chunkStart)
	if err != nil {
		return 0, err
	}
	// size is known after any fetch
	if c.offset >= c.size {
		return 0, io.EOF
	}

	within := c.offset - chunkStart
	if within >= int64(len(chunk)) {
		// Short chunk that doesn't reach our offset (truncated file / lying server).
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, chunk[within:])
	c.offset += int64(n)
	return n, nil
}

func (c *ChunkedHttpReadSeeker) Seek(offset int64, whence int) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = c.offset + offset
	case io.SeekEnd:
		if c.size < 0 {
			if err := c.discoverSize(); err != nil {
				return c.offset, err
			}
		}
		next = c.size + offset
	default:
		return c.offset, fmt.Errorf("chunkedrs: invalid whence %d", whence)
	}
	if next < 0 {
		return c.offset, fmt.Errorf("chunkedrs: negative position")
	}
	c.offset = next
	return c.offset, nil
}

func (c *ChunkedHttpReadSeeker) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.chunks = nil
	c.order = nil
	return nil
}

// chunkAt returns the chunk starting at the (aligned) offset, fetching + caching it if absent.
// Caller holds c.mu.
func (c *ChunkedHttpReadSeeker) chunkAt(chunkStart int64) ([]byte, error) {
	if b, ok := c.chunks[chunkStart]; ok {
		return b, nil
	}
	b, err := c.fetchRange(chunkStart, chunkStart+c.chunkSize-1)
	if err != nil {
		return nil, err
	}
	c.chunks[chunkStart] = b
	c.order = append(c.order, chunkStart)
	for len(c.order) > c.maxChunks {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.chunks, evict)
	}
	return b, nil
}

// discoverSize learns the total size with a 1-byte range request (Content-Range total).
func (c *ChunkedHttpReadSeeker) discoverSize() error {
	_, err := c.fetchRange(0, 0) // sets c.size from Content-Range; tiny body discarded (not cached)
	return err
}

// fetchRange GETs [start,end] with the same transient-retry policy as HttpReadSeeker
// (httprsTransientStatus / httprsBackoff / StatusError). Updates c.size from the response.
func (c *ChunkedHttpReadSeeker) fetchRange(start, end int64) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < httprsMaxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodGet, c.url, nil)
		if err != nil {
			return nil, err
		}
		overrideHeaders(req.Header, c.headers)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			if attempt == httprsMaxRetries-1 {
				return nil, err
			}
			time.Sleep(httprsBackoff(attempt, ""))
			continue
		}

		if httprsTransientStatus(resp.StatusCode) {
			retryAfter := resp.Header.Get("Retry-After")
			resp.Body.Close()
			lastErr = &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter}
			if attempt == httprsMaxRetries-1 {
				return nil, lastErr
			}
			time.Sleep(httprsBackoff(attempt, retryAfter))
			continue
		}

		switch resp.StatusCode {
		case http.StatusPartialContent:
			if total, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok {
				c.size = total
			}
			b, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr != nil {
				lastErr = rerr
				if attempt == httprsMaxRetries-1 {
					return nil, rerr
				}
				time.Sleep(httprsBackoff(attempt, ""))
				continue
			}
			return b, nil
		case http.StatusOK:
			// Server ignored the Range header. Only usable when we asked from 0 — take up to a
			// chunk of the body and learn the size from Content-Length.
			if start != 0 {
				resp.Body.Close()
				return nil, fmt.Errorf("chunkedrs: server does not support range requests")
			}
			if resp.ContentLength >= 0 {
				c.size = resp.ContentLength
			}
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, end-start+1))
			resp.Body.Close()
			if rerr != nil {
				return nil, rerr
			}
			return b, nil
		case http.StatusRequestedRangeNotSatisfiable:
			// Past EOF (e.g. last chunk request beyond the file) — learn size if advertised.
			if total, ok := contentRangeTotal(resp.Header.Get("Content-Range")); ok {
				c.size = total
			}
			resp.Body.Close()
			return []byte{}, nil
		default:
			retryAfter := resp.Header.Get("Retry-After")
			resp.Body.Close()
			return nil, &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter}
		}
	}
	return nil, lastErr
}

// contentRangeTotal parses the total size out of "bytes 0-1023/4096" (or "bytes */4096").
func contentRangeTotal(h string) (int64, bool) {
	if h == "" {
		return 0, false
	}
	i := strings.LastIndexByte(h, '/')
	if i < 0 || i == len(h)-1 {
		return 0, false
	}
	totalStr := h[i+1:]
	if totalStr == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(totalStr), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
