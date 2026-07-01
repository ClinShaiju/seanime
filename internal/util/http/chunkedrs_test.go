package httputil

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// rangeServer serves `data` with Range support and counts requests.
func rangeServer(t *testing.T, data []byte, reqCount *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
			return
		}
		var start, end int64
		_, err := fmt.Sscanf(strings.TrimPrefix(rng, "bytes="), "%d-%d", &start, &end)
		if err != nil || start >= int64(len(data)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
}

func testPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// A metadata-parse-like access pattern (head reads, tail seek, revisit head) must produce
// only a few chunk requests, read the right bytes, and never refetch a cached region.
func TestChunkedReadSeeker_PatternAndRequestCount(t *testing.T) {
	data := testPattern(5 << 20) // 5MiB file
	var reqs atomic.Int64
	srv := rangeServer(t, data, &reqs)
	defer srv.Close()

	c := NewChunkedHttpReadSeeker(srv.URL, nil)
	c.chunkSize = 1 << 20 // 1MiB chunks for the test
	c.maxChunks = 3
	defer c.Close()

	// Head read
	head := make([]byte, 4096)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(head, data[:4096]) {
		t.Fatal("head bytes mismatch")
	}

	// Tail seek (cues) + read
	if _, err := c.Seek(-2048, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	tail := make([]byte, 2048)
	if _, err := io.ReadFull(c, tail); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tail, data[len(data)-2048:]) {
		t.Fatal("tail bytes mismatch")
	}

	// Revisit head (cached — must not refetch)
	before := reqs.Load()
	if _, err := c.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	again := make([]byte, 1024)
	if _, err := io.ReadFull(c, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data[100:1124]) {
		t.Fatal("revisit bytes mismatch")
	}
	if reqs.Load() != before {
		t.Fatalf("revisiting a cached chunk refetched (reqs %d -> %d)", before, reqs.Load())
	}

	// Whole pattern should have cost: head chunk (1) + tail chunk (1) = 2 requests.
	if got := reqs.Load(); got > 2 {
		t.Fatalf("expected <=2 range requests for head+tail pattern, got %d", got)
	}
}

// A cross-chunk sequential read (attachments region) returns correct bytes and one request per chunk.
func TestChunkedReadSeeker_CrossChunkSequential(t *testing.T) {
	data := testPattern(3 << 20)
	var reqs atomic.Int64
	srv := rangeServer(t, data, &reqs)
	defer srv.Close()

	c := NewChunkedHttpReadSeeker(srv.URL, nil)
	c.chunkSize = 1 << 20
	defer c.Close()

	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("full read mismatch")
	}
	if reqs.Load() != 3 {
		t.Fatalf("expected 3 chunk requests for a 3-chunk file, got %d", reqs.Load())
	}
}

// A transient 429 with Retry-After is retried, not surfaced as content or a permanent error.
func TestChunkedReadSeeker_RetriesTransient(t *testing.T) {
	data := testPattern(64 * 1024)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	c := NewChunkedHttpReadSeeker(srv.URL, nil)
	defer c.Close()

	got := make([]byte, 1024)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[:1024]) {
		t.Fatal("bytes after retry mismatch")
	}
	if hits.Load() != 2 {
		t.Fatalf("expected exactly one retry (2 hits), got %d", hits.Load())
	}
}

// EOF behavior: reading past the end returns io.EOF, and SeekEnd works without a prior read.
func TestChunkedReadSeeker_EOFAndSeekEnd(t *testing.T) {
	data := testPattern(10_000)
	var reqs atomic.Int64
	srv := rangeServer(t, data, &reqs)
	defer srv.Close()

	c := NewChunkedHttpReadSeeker(srv.URL, nil)
	defer c.Close()

	if pos, err := c.Seek(0, io.SeekEnd); err != nil || pos != int64(len(data)) {
		t.Fatalf("SeekEnd got (%d,%v)", pos, err)
	}
	buf := make([]byte, 10)
	if _, err := c.Read(buf); err != io.EOF {
		t.Fatalf("expected EOF at end, got %v", err)
	}
}
