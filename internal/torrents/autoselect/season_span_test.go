package autoselect

import (
	"testing"

	hibiketorrent "seanime/internal/extension/hibike/torrent"
)

// "S02-20" in an aggregator display name is season 2 / EPISODE 20, not the season range 2..20.
// habari reports both halves as seasons, so buildCandidates repairs the parse before any gate
// reads it: a season-4 request must miss it, a season-2 episode-14 request must miss it too, and
// a real "S1 - S4" pack must still cover its mid-seasons.
func TestSeasonEpisodeSpanParse(t *testing.T) {
	const rezeroDub = "[TB] Comet 1080p\n📁 Re Zero -starting Life In Another World S02-20\n🎞️ AVC 🏷️ Golumpa\n🌐 🇬🇧 / Dubbed"

	cases := []struct {
		name        string
		season      int
		episode     int
		wantSeasons []int
		wantEpisode bool // requested episode is covered
		wantSeason  bool // requested season is covered
	}{
		{rezeroDub, 4, 14, []int{2}, false, false},
		{rezeroDub, 2, 14, []int{2}, false, true},
		{rezeroDub, 2, 20, []int{2}, true, true},
		{"[Golumpa] Re ZERO -Starting Life in Another World- Season 2 - 20 [English Dub]", 4, 14, []int{2}, false, false},
		{"Re Zero - Starting Life In Another World S04 • E14", 4, 14, []int{4}, true, true},
		{"[Judas] Some Show S1 - S4 (Batch)", 3, 5, []int{1, 4}, true, true},
		{"[Judas] Some Show S02 - 1080p WEB-DL", 2, 5, []int{2}, true, true},
	}

	for _, tc := range cases {
		c := buildCandidates([]*hibiketorrent.AnimeTorrent{{Name: tc.name}}, tc.season, tc.episode, 0)[0]

		if got := declaredSeasons(c); !equalInts(got, tc.wantSeasons) {
			t.Errorf("%q: declaredSeasons = %v, want %v", tc.name, got, tc.wantSeasons)
		}
		if got := seasonCovered(declaredSeasons(c), tc.season, isUnlabeledSeasonPack(c)); got != tc.wantSeason {
			t.Errorf("%q: seasonCovered(%d) = %v, want %v", tc.name, tc.season, got, tc.wantSeason)
		}
		if got := episodeCovered(c.parsed.EpisodeNumber, tc.episode); got != tc.wantEpisode {
			t.Errorf("%q: episodeCovered(%d) = %v (parsed %v), want %v", tc.name, tc.episode, got, c.parsed.EpisodeNumber, tc.wantEpisode)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
