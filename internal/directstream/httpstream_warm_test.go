package directstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWindow(t *testing.T) {
	s := &httpBaseStream{}
	s.contentLength = 100 << 20 // 100MiB

	// Head window: [0, warmHeadBytes)
	require.Equal(t, warmHeadBytes, s.handoffWindow(0))
	require.Equal(t, warmHeadBytes, s.handoffWindow(warmHeadBytes-1))

	// Between the windows → redirect
	require.Equal(t, int64(0), s.handoffWindow(warmHeadBytes))
	require.Equal(t, int64(0), s.handoffWindow(s.contentLength-warmTailBytes-1))

	// Tail window: [len-warmTailBytes, len) serves to EOF (no truncation)
	require.Equal(t, s.contentLength, s.handoffWindow(s.contentLength-warmTailBytes))
	require.Equal(t, s.contentLength, s.handoffWindow(s.contentLength-1))

	// Small file fully covered by the windows: always in-window, never redirected
	small := &httpBaseStream{}
	small.contentLength = warmHeadBytes + warmTailBytes - 1
	require.Equal(t, small.contentLength, small.handoffWindow(0))
	require.Equal(t, small.contentLength, small.handoffWindow(small.contentLength-1))
}

func TestPrewarmedWindowBytes(t *testing.T) {
	const url = "https://cdn.test/file.mkv"
	defer prewarmWindowCache.Delete(url)

	// No entry
	require.Nil(t, prewarmedWindowBytes(url, 0, 10))

	head := make([]byte, 100)
	tailStart := int64(9000)
	tail := make([]byte, 50)
	prewarmWindowCache.Set(url, map[int64][]byte{0: head, tailStart: tail})

	// Exact and shorter lengths hit (sliced), longer misses, wrong offset misses
	require.Len(t, prewarmedWindowBytes(url, 0, 100), 100)
	require.Len(t, prewarmedWindowBytes(url, 0, 40), 40)
	require.Nil(t, prewarmedWindowBytes(url, 0, 101))
	require.Nil(t, prewarmedWindowBytes(url, 1, 10))
	require.Len(t, prewarmedWindowBytes(url, tailStart, 50), 50)
}
