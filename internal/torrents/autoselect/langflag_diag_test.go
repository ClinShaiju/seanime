package autoselect

import (
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"testing"
)

// Diagnostic: does Seanime credit English when a release expresses languages ONLY as flag
// emoji (🇬🇧/🇯🇵) in the name (AIOStreams 'formatter' output)? Uses the real SeaDex-best name
// captured from the live aggregator. If SEADEX lands in the dub band (band 3), the
// flags-in-name path works and the live burial is the 'filename' result format omitting them.
func TestLangFlag_SeaDexFormatterName(t *testing.T) {
	gb := "\U0001F1EC\U0001F1E7" // 🇬🇧
	jp := "\U0001F1EF\U0001F1F5" // 🇯🇵
	seadexName := "[TB☁️⚡] SeaDex 1080p (Best)\n" +
		"Mahouka Koukou No Rettousei E04\nBluRay HEVC sam\nFLAC AAC 2.0\n" +
		"1.99 GB / 55.3 GB Nyaa\n" + gb + " / " + jp + " " + gb
	emberName := "Nyaa.si 1080p The Irregular At Magic High School (2014-2020) S01-02 BluRay HEVC EMBER 465 MB Dual Audio"

	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, eng, english", "jp, jpn, japanese"},
		PreferredCodecs:    []string{"HEVC, x265, H.265, 10-bit, 10 bit, 10bit"},
		PreferredSources:   []string{"BDRip, BD RIP, BluRay, Blu-Ray, Blu Ray, BD"},
	}

	t.Logf("LanguagesFromFlags(seadex) = %v", util.LanguagesFromFlags(seadexName))

	s := &AutoSelect{}
	cands := buildCandidates([]*hibiketorrent.AnimeTorrent{{Name: seadexName}, {Name: emberName}}, 0, 0)
	seadex, ember := cands[0], cands[1]
	seadexScore := s.calculateScore(seadex, profile)
	emberScore := s.calculateScore(ember, profile)
	t.Logf("SEADEX score=%d band=%d res=%q | EMBER score=%d band=%d res=%q",
		seadexScore, scoreBand(seadexScore), seadex.parsed.VideoResolution,
		emberScore, scoreBand(emberScore), ember.parsed.VideoResolution)

	// When the flags are in the name, both are English-dub band...
	if scoreBand(seadexScore) != 3 {
		t.Fatalf("SEADEX should be in the English-dub band (3), got %d", scoreBand(seadexScore))
	}
	// ...and the genuinely-better SeaDex (FLAC + correctly-credited resolution) must outrank the
	// barebones EMBER. Regression guard for the resolution name-fallback fix.
	if seadexScore <= emberScore {
		t.Fatalf("SEADEX (%d) should outrank EMBER (%d) — resolution name-fallback regressed?", seadexScore, emberScore)
	}
}

// Priority rule: English is primary (band); within English, REMUX wins; a non-English REMUX
// must never beat an English non-REMUX.
func TestPriority_RemuxWithinEnglish(t *testing.T) {
	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, eng, english", "jp, jpn, japanese"},
		PreferredCodecs:    []string{"HEVC, x265, H.265"},
		PreferredSources:   []string{"BluRay, Blu-Ray, BD"},
	}
	jp := "\U0001F1EF\U0001F1F5" // 🇯🇵 (Japanese-only)
	engRemux := "Show 1080p BluRay REMUX HEVC Group Dual Audio"
	engNonRemux := "Show 1080p BluRay HEVC FLAC AAC Group Dual Audio"
	jpRemux := "Show 1080p BluRay REMUX HEVC Group " + jp

	s := &AutoSelect{}
	cands := buildCandidates([]*hibiketorrent.AnimeTorrent{{Name: engRemux}, {Name: engNonRemux}, {Name: jpRemux}}, 0, 0)
	er, enr, jr := s.calculateScore(cands[0], profile), s.calculateScore(cands[1], profile), s.calculateScore(cands[2], profile)
	t.Logf("engRemux=%d engNonRemux=%d jpRemux=%d", er, enr, jr)

	if !(er > enr) {
		t.Fatalf("English REMUX (%d) must beat English non-REMUX (%d)", er, enr)
	}
	if !(enr > jr) {
		t.Fatalf("English non-REMUX (%d) must beat Japanese-only REMUX (%d) — band must dominate REMUX bonus", enr, jr)
	}
}
