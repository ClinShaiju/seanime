package debrid_client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A CDN can answer 200/206 with a truncated file or an error page. probeStreamURLWithSize must call
// those DEAD, while keeping the existing rule that throttling/transient 5xx are ALIVE (treating a
// 429 as dead re-resolves exactly when the CDN asked us to back off).
func TestProbeStreamURLWithSize(t *testing.T) {
	const fullSize = 1427264036 // the real episode size TorBox's API reported
	const truncated = 12360092  // what its CDN actually served (0.87%)

	newSrv := func(h http.HandlerFunc) *httptest.Server {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s
	}

	video := func(total int64) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.FormatInt(total, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0x1a})
		}
	}

	tests := []struct {
		name         string
		handler      http.HandlerFunc
		expectedSize int64
		wantAlive    bool
	}{
		{"full file matches expected size", video(fullSize), fullSize, true},
		{"truncated file is dead", video(truncated), fullSize, false},
		{"truncated file passes when size unknown", video(truncated), 0, true},
		{"unknown total (bytes 0-0/*) is not a mismatch", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", "bytes 0-0/*")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0x1a})
		}, fullSize, true},
		{"html error page dressed as 200 is dead", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><head><title>404 Not Found</title></head></html>"))
		}, 0, false},
		{"429 is ALIVE (do not amplify throttling)", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}, fullSize, true},
		{"503 is ALIVE (transient)", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, fullSize, true},
		{"404 is dead", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSrv(tt.handler)
			got := probeStreamURLWithSize(context.Background(), srv.URL, 5*time.Second, tt.expectedSize)
			require.Equal(t, tt.wantAlive, got)
		})
	}

	t.Run("empty url is dead", func(t *testing.T) {
		require.False(t, probeStreamURLWithSize(context.Background(), "", time.Second, 0))
	})
}

func TestParseContentRangeTotal(t *testing.T) {
	for _, tt := range []struct {
		in     string
		want   int64
		wantOk bool
	}{
		{"bytes 0-0/1427264036", 1427264036, true},
		{"bytes 0-0/*", 0, false},
		{"", 0, false},
		{"garbage", 0, false},
		{"bytes 0-0/0", 0, false},
	} {
		got, ok := parseContentRangeTotal(tt.in)
		require.Equal(t, tt.wantOk, ok, tt.in)
		require.Equal(t, tt.want, got, tt.in)
	}
}
