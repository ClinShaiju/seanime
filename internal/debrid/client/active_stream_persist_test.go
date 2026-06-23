package debrid_client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPersistedActiveStream_JSONRoundTrip verifies the active-stream snapshot serializes and
// deserializes losslessly — the data that survives a server restart to enable instant
// reconnect. A broken round-trip would silently fall back to a full re-resolve.
func TestPersistedActiveStream_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	orig := persistedActiveStream{
		Opts: &StartStreamOptions{
			MediaId:       123,
			EpisodeNumber: 5,
			AniDBEpisode:  "5",
			UserID:        7,
			PlaybackType:  PlaybackTypeNativePlayer,
			AutoSelect:    true,
		},
		StreamUrl:     "https://cdn.example/dld/abc?token=xyz",
		FileId:        "file-1",
		Filepath:      "/Season 1/Episode 05.mkv",
		TorrentItemId: "torrent-item-1",
		ResolvedAt:    now,
		UrlResolvedAt: now,
		TtlNanos:      int64(time.Hour),
	}

	data, err := json.Marshal(&orig)
	require.NoError(t, err)

	var got persistedActiveStream
	require.NoError(t, json.Unmarshal(data, &got))

	require.NotNil(t, got.Opts)
	require.Equal(t, orig.Opts.MediaId, got.Opts.MediaId)
	require.Equal(t, orig.Opts.EpisodeNumber, got.Opts.EpisodeNumber)
	require.Equal(t, orig.Opts.UserID, got.Opts.UserID)
	require.Equal(t, orig.StreamUrl, got.StreamUrl)
	require.Equal(t, orig.TorrentItemId, got.TorrentItemId)
	require.Equal(t, orig.FileId, got.FileId)
	require.Equal(t, orig.Filepath, got.Filepath)
	require.Equal(t, orig.TtlNanos, got.TtlNanos)
	require.True(t, got.ResolvedAt.Equal(orig.ResolvedAt))

	// The reconstructed preload key must match the original opts so a re-issue hits the cache.
	require.Equal(t, preloadKey(orig.Opts), preloadKey(got.Opts))
}
