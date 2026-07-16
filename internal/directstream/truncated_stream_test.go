package directstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A debrid CDN can answer 200 with a truncated file (TorBox served 12,360,092 bytes of a
// 1,427,264,036-byte episode for a torrent still reporting cached=true). The container header still
// declares the full duration, so it plays ~12s and then can't seek — this guard aborts the open
// instead, and must stay inert when no size is known.
func TestTruncatedStreamErr(t *testing.T) {
	const expected = 1427264036
	const served = 12360092

	t.Run("truncated file errors and names the numbers", func(t *testing.T) {
		err := truncatedStreamErr(served, expected)
		require.Error(t, err)
		require.Contains(t, err.Error(), "12360092")
		require.Contains(t, err.Error(), "1427264036")
		require.Contains(t, err.Error(), "0.87%")
	})

	t.Run("exact match is fine", func(t *testing.T) {
		require.NoError(t, truncatedStreamErr(expected, expected))
	})

	t.Run("inert when the provider size is unknown", func(t *testing.T) {
		require.NoError(t, truncatedStreamErr(served, 0))
	})

	t.Run("inert when the content length wasn't fetched", func(t *testing.T) {
		require.NoError(t, truncatedStreamErr(0, expected))
	})
}
