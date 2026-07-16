package directstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"seanime/internal/mkvparser"
	"seanime/internal/player"
	"seanime/internal/util"
	"seanime/internal/util/result"

	"github.com/samber/mo"
	"github.com/stretchr/testify/require"
)

func newHTTPStreamTestManager() *Manager {
	return &Manager{
		Logger:      util.NewLogger(),
		playbackCtx: context.Background(),
	}
}

func newTestNakamaStream(manager *Manager, streamURL string, token string) *Nakama {
	return &Nakama{
		httpBaseStream: httpBaseStream{
			streamUrl: streamURL,
			requestHeaders: http.Header{
				"X-Seanime-Nakama-Token": []string{token},
			},
			headResponseHeaders: http.Header{
				"X-Seanime-Nakama-Token": []string{token},
			},
			BaseStream: BaseStream{
				manager:               manager,
				logger:                manager.Logger,
				subtitleEventCache:    result.NewMap[string, *mkvparser.SubtitleEvent](),
				activeSubtitleStreams: result.NewMap[string, *SubtitleStream](),
			},
		},
	}
}

func TestNakamaLoadContentTypeUsesSharedRequestHeaders(t *testing.T) {
	const token = "nakama-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))
		require.Equal(t, http.MethodHead, r.Method)

		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "6")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mp4", token)

	require.Equal(t, "video/mp4", stream.LoadContentType())
	require.Equal(t, int64(6), stream.contentLength)
}

func TestNakamaGetStreamHandlerPreservesHeadResponseToken(t *testing.T) {
	const token = "nakama-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))
		require.Equal(t, http.MethodHead, r.Method)

		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "6")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mp4", token)
	stream.playbackInfo = &player.PlaybackInfo{MkvMetadataParser: mo.None[*mkvparser.MetadataParser]()}

	require.Equal(t, "video/mp4", stream.LoadContentType())

	req := httptest.NewRequest(http.MethodHead, "/api/v1/directstream/stream", nil)
	rec := httptest.NewRecorder()

	stream.GetStreamHandler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, token, rec.Header().Get("X-Seanime-Nakama-Token"))
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	require.Equal(t, "6", rec.Header().Get("Content-Length"))
}

func TestNakamaGetStreamHandlerProxiesWithSharedRequestHeaders(t *testing.T) {
	const token = "nakama-secret"
	payload := []byte("abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))

		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			require.Equal(t, "bytes=0-3", r.Header.Get("Range"))
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "4")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-3/%d", len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:4])
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mp4", token)
	stream.playbackInfo = &player.PlaybackInfo{MkvMetadataParser: mo.None[*mkvparser.MetadataParser]()}

	require.Equal(t, "video/mp4", stream.LoadContentType())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/directstream/stream", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()

	stream.GetStreamHandler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "abcd", rec.Body.String())
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
}

func TestNakamaReadAheadServesFromCacheAndFillsAhead(t *testing.T) {
	// Force the opt-in read-ahead path on for this test (bypassing the env-gated sync.Once).
	readAheadOnce.Do(func() {})
	readAheadOn = true
	defer func() { readAheadOn = false }()

	const token = "nakama-secret"
	payload := []byte("abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		// Open-ended range from N → 206 with payload[N:]. The producer fills the cache from this body.
		start := 0
		if rng := r.Header.Get("Range"); rng != "" {
			s := strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-")
			var err error
			start, err = strconv.Atoi(s)
			require.NoError(t, err)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start:])
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mp4", token)
	stream.playbackInfo = &player.PlaybackInfo{MkvMetadataParser: mo.None[*mkvparser.MetadataParser]()}
	require.Equal(t, "video/mp4", stream.LoadContentType())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/directstream/stream", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	stream.GetStreamHandler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "abcdef", rec.Body.String(), "client is served the full range from the cache")
	// The producer filled the cache (read-ahead infrastructure) while serving.
	require.Eventually(t, func() bool {
		return stream.httpStream != nil && stream.httpStream.IsRangeAvailable(0, int64(len(payload)-1))
	}, 2*time.Second, 10*time.Millisecond, "producer should fill the cache")
}

func TestNakamaMetadataReaderCarriesHeadersAcrossRangeRequests(t *testing.T) {
	const token = "nakama-secret"
	payload := []byte("abcdef")
	requestCount := atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
			return
		}

		// Accept both open-ended ("bytes=N-") and bounded ("bytes=N-M") ranges — the chunked
		// metadata reader sends bounded ones.
		spec := strings.TrimPrefix(rangeHeader, "bytes=")
		startStr, endStr, _ := strings.Cut(spec, "-")
		start, err := strconv.Atoi(startStr)
		require.NoError(t, err)
		end := len(payload) - 1
		if endStr != "" {
			if e, eerr := strconv.Atoi(endStr); eerr == nil && e < end {
				end = e
			}
		}

		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mkv", token)

	reader, err := stream.newMetadataReader()
	require.NoError(t, err)
	defer reader.Close()
	require.Zero(t, requestCount.Load())

	first := make([]byte, 3)
	_, err = io.ReadFull(reader, first)
	require.NoError(t, err)
	require.Equal(t, "abc", string(first))

	_, err = reader.Seek(2, io.SeekStart)
	require.NoError(t, err)

	second := make([]byte, 3)
	_, err = io.ReadFull(reader, second)
	require.NoError(t, err)
	require.Equal(t, "cde", string(second))
}

func TestNakamaMetadataReaderStartsAtRequestedRange(t *testing.T) {
	const token = "nakama-secret"
	payload := []byte("abcdefgh")
	ranges := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Seanime-Nakama-Token"))
		ranges <- r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 4-7/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[4:])
	}))
	defer server.Close()

	manager := newHTTPStreamTestManager()
	stream := newTestNakamaStream(manager, server.URL+"/video.mkv", token)
	reader, err := stream.newMetadataReader()
	require.NoError(t, err)
	defer reader.Close()

	_, err = reader.Seek(4, io.SeekStart)
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(reader, buf)
	require.NoError(t, err)
	require.Equal(t, "efgh", string(buf))
	require.Equal(t, "bytes=4-", <-ranges)
}
