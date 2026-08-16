package util

import (
	"strings"
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

// The real Debridio/AIOStreams name: multi-line, emoji, "•" separator, trailing size. Raw, habari
// reads the size as the episode and drops the real one; CleanReleaseName recovers season+episode.
func TestCleanReleaseName_AggregatorName(t *testing.T) {
	raw := "Debridio Scraper 1080p\n📁 Witch Hat Atelier (2026) S01 • E12\n🎥 WEB-DL 🏷️ Dual\n📦 833 MB 🔍 DHT\n🌐 EN/JP"

	assert.Equal(t, []string{"833"}, habari.Parse(raw).EpisodeNumber, "raw name misparses the size as episode")

	m := habari.Parse(CleanReleaseName(raw))
	assert.Equal(t, []string{"12"}, m.EpisodeNumber, "episode recovered")
	assert.Equal(t, []string{"01"}, m.SeasonNumber, "season recovered")
}

func TestLanguagesFromFlags(t *testing.T) {
	assert.Contains(t, LanguagesFromFlags("Show E10 🌐 🇬🇧 / 🇯🇵"), "english")
	assert.Contains(t, LanguagesFromFlags("Show E10 🌐 🇬🇧 / 🇯🇵"), "japanese")
	assert.Contains(t, LanguagesFromFlags("Show E10 🌐 🇫🇷"), "french")
	assert.Contains(t, LanguagesFromFlags("Show E10 🌐 🇪🇸"), "spanish")
	assert.Empty(t, LanguagesFromFlags("Show E10 1080p no flags here"))
}

// TestDeriveAudioLanguages_MatchesBadgeExpectations pins the audio-vs-subtitle split that both
// auto-select ranking and the torrent-list badges read. Each case is a real release name; the
// expectation is the AUDIO language list the picker badges "Original + Dub" / "Dubbed" from.
func TestDeriveAudioLanguages_MatchesBadgeExpectations(t *testing.T) {
	cases := []struct {
		name      string
		release   string
		wantAudio []string
	}{
		{
			// Japanese audio, ENG/POR-BR/SPA-LA SUBTITLES. Must yield no audio languages at all:
			// crediting them badged this "Dubbed" and would rank it as a foreign dub.
			name:      "subtitle languages are not audio",
			release:   "[Erai-raws] Show - 07 [1080p][Multiple Subtitle] [ENG][POR-BR][SPA-LA]",
			wantAudio: nil,
		},
		{
			// Japanese audio with CHINESE subtitles, expressed as audio flags. Both languages are
			// reported, but English is absent — so no dub badge, and ranking demotes it.
			name:      "jp/cn flags stay two foreign-market languages",
			release:   "[TB] Nyaa.si 1080p\n📁 Kakekoi E04\n🏷️ LoliHouse\n🌐 🇯🇵 / 🇨🇳",
			wantAudio: []string{"Japanese", "Chinese"},
		},
		{
			// Audio flags before the 📝 marker are English+Japanese; the subtitle flags after it
			// must not leak in.
			name:      "audio flags win over subtitle flags",
			release:   "[TB] SeaDex 1080p (Best)\n📁 Show S01 • E12\n🌐 🇬🇧 / 🇯🇵📝 🇬🇧 / 🇸🇦 / 🇫🇷",
			wantAudio: []string{"English", "Japanese"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cleaned := CleanReleaseName(tt.release)
			parsed := habari.Parse(cleaned)
			got, _ := DeriveAudioLanguages(
				DisplayLanguagesFromFlags(tt.release),
				parsed.Language,
				parsed.Subtitles,
				IsDualAudioRelease(parsed.AudioTerm, strings.ToLower(cleaned)),
			)
			assert.Equal(t, tt.wantAudio, got)
		})
	}
}

// TestIsServiceMultiAudio covers the one case where "Multi Audio" really does mean the Japanese
// original plus an English dub: a Western streaming service. The French scene "MULTi" must not.
func TestIsServiceMultiAudio(t *testing.T) {
	crunchyroll := strings.ToLower(CleanReleaseName("📁 Mushoku Tensei S03 • E01\n🏷️ VARYG📡 Crunchyroll \n🔍 Multi Subs|Multi Audio"))
	assert.True(t, IsServiceMultiAudio([]string{"Multi Audio"}, crunchyroll))

	frenchScene := strings.ToLower("Nisekoi.S01E04.MULTi.1080p.BluRay.x264-SHiNiGAMi")
	assert.False(t, IsServiceMultiAudio(nil, frenchScene), "scene MULTi is French, not an English dub")

	multiSubsOnly := strings.ToLower(CleanReleaseName("📁 Show S01 • E04\n🏷️ ToonsHub📡 Crunchyroll\n🔍 Multi Subs"))
	assert.False(t, IsServiceMultiAudio(nil, multiSubsOnly), "multi SUBS is not multi audio")
}
