package util

import (
	"testing"

	"github.com/5rahim/habari"
	"github.com/stretchr/testify/assert"
)

func TestStripSizeTokens(t *testing.T) {
	cases := map[string]string{
		"Witch Hat Atelier (2026) 833 MB":  "Witch Hat Atelier (2026)  ",
		"Show 950MB":                       "Show  ",
		"Show 4.94 GB batch":               "Show   batch",
		"Show 227.2 MiB":                   "Show  ",
		"Show S01E12 1080p HEVC 10bit x265": "Show S01E12 1080p HEVC 10bit x265", // nothing stripped
	}
	for in, want := range cases {
		assert.Equal(t, want, StripSizeTokens(in), "input: %q", in)
	}
}

// Confirms the strip fixes the size-as-episode misparse while preserving real episodes and codecs.
func TestStripSizeTokens_FixesEpisodeMisparse(t *testing.T) {
	// Bare size becomes the episode without stripping; stripping removes the false episode.
	assert.Equal(t, []string{"833"}, habari.Parse("Witch Hat Atelier (2026) 833 MB").EpisodeNumber)
	assert.Empty(t, habari.Parse(StripSizeTokens("Witch Hat Atelier (2026) 833 MB")).EpisodeNumber)

	// A real episode + codec survive stripping.
	m := habari.Parse(StripSizeTokens("Tongari Boushi No Atelier (2026) S01 E12 WEBRip HEVC 950 MB"))
	assert.Equal(t, []string{"12"}, m.EpisodeNumber)
	assert.Contains(t, m.VideoTerm, "HEVC")
}
