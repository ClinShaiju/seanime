package autoselect

import (
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func newTestAutoSelect() *AutoSelect {
	logger := zerolog.Nop()
	return New(&NewAutoSelectOptions{
		Logger: &logger,
	})
}

func TestAutoSelect_Filter(t *testing.T) {
	s := newTestAutoSelect()

	t1 := &hibiketorrent.AnimeTorrent{Name: "[SubsPlease] One Piece - 1000 (1080p).mkv", Seeders: 100, Size: 1024 * 1024 * 1000}                             // 1GB
	t2 := &hibiketorrent.AnimeTorrent{Name: "[Erai-raws] One Piece - 1000 [720p][Multiple Subtitle].mkv", Seeders: 50, Size: 1024 * 1024 * 500}              // 500MB
	t3 := &hibiketorrent.AnimeTorrent{Name: "[EMBER] One Piece - 1000 [1080p] [Dual Audio] [HEVC].mkv", Seeders: 200, Size: 1024 * 1024 * 800}               // 800MB
	t4 := &hibiketorrent.AnimeTorrent{Name: "[French-Fansub] One Piece - 1000 [480p] [French].mkv", Seeders: 10, Size: 1024 * 1024 * 200}                    // 200MB
	t5 := &hibiketorrent.AnimeTorrent{Name: "[Cleo] One Piece - Batch [1080p] [Dual Audio].mkv", Seeders: 500, Size: 1024 * 1024 * 1024 * 50, IsBatch: true} // 50GB Batch
	t6 := &hibiketorrent.AnimeTorrent{Name: "[Judas] One Piece - 1000 [1080p][HEVC x265 10bit].mkv", Seeders: 150, Size: 1024 * 1024 * 600}                  // 600MB
	t7 := &hibiketorrent.AnimeTorrent{Name: "[HorribleSubs] One Piece - 1000 [720p].mkv", Seeders: 20, Size: 1024 * 1024 * 400}                              // 400MB
	t8 := &hibiketorrent.AnimeTorrent{Name: "[SourceCheck] One Piece - 1000 [Web-DL].mkv", Seeders: 30, Size: 1024 * 1024 * 900}                             // 900MB

	torrents := []*hibiketorrent.AnimeTorrent{t1, t2, t3, t4, t5, t6, t7, t8}

	tests := []struct {
		name     string
		profile  *anime.AutoSelectProfile
		expected []string // Names of expected torrents
	}{
		{
			name:     "No profile should return all",
			profile:  nil,
			expected: []string{t1.Name, t2.Name, t3.Name, t4.Name, t5.Name, t6.Name, t7.Name, t8.Name},
		},
		{
			name: "Min Seeders > 50",
			profile: &anime.AutoSelectProfile{
				MinSeeders: 51,
			},
			expected: []string{t1.Name, t3.Name, t5.Name, t6.Name}, // t1(100), t3(200), t5(500), t6(150)
		},
		{
			name: "Resolution exclusion",
			profile: &anime.AutoSelectProfile{
				ExcludeTerms: []string{"480p", "720p"},
			},
			expected: []string{t1.Name, t3.Name, t5.Name, t6.Name, t8.Name},
		},
		{
			name: "Require language (French)",
			profile: &anime.AutoSelectProfile{
				RequireLanguage:    true,
				PreferredLanguages: []string{"fr, french"},
			},
			expected: []string{t4.Name},
		},
		{
			name: "Require language (English)",
			profile: &anime.AutoSelectProfile{
				RequireLanguage:    true,
				PreferredLanguages: []string{"English"},
			},
			expected: []string{},
		},
		{
			name: "Min size 600MB",
			profile: &anime.AutoSelectProfile{
				MinSize: "600MB",
			},
			expected: []string{t1.Name, t3.Name, t5.Name, t6.Name, t8.Name},
		},
		{
			name: "Max size 700MB",
			profile: &anime.AutoSelectProfile{
				MaxSize: "700MB",
			},
			expected: []string{t2.Name, t4.Name, t6.Name, t7.Name},
		},
		{
			name: "Require codec HEVC",
			profile: &anime.AutoSelectProfile{
				RequireCodec:    true,
				PreferredCodecs: []string{"HEVC", "x265"},
			},
			expected: []string{t3.Name, t6.Name},
		},
		{
			name: "Required source Web-DL",
			profile: &anime.AutoSelectProfile{
				RequireSource:    true,
				PreferredSources: []string{"Web-DL"},
			},
			expected: []string{t8.Name}, // assuming habari detects Web-DL properly or fallback check works
		},
		{
			name: "Source token should not match inside another word",
			profile: &anime.AutoSelectProfile{
				RequireSource:    true,
				PreferredSources: []string{"CR"},
			},
			expected: []string{},
		},
		{
			name: "Dual audio only",
			profile: &anime.AutoSelectProfile{
				MultipleAudioPreference: anime.AutoSelectPreferenceOnly,
			},
			expected: []string{t3.Name, t5.Name},
		},
		{
			name: "Dual audio never",
			profile: &anime.AutoSelectProfile{
				MultipleAudioPreference: anime.AutoSelectPreferenceNever,
			},
			expected: []string{t1.Name, t2.Name, t4.Name, t6.Name, t7.Name, t8.Name},
		},
		{
			name: "Batch only",
			profile: &anime.AutoSelectProfile{
				BatchPreference: anime.AutoSelectPreferenceOnly,
			},
			expected: []string{t5.Name},
		},
		{
			name: "Batch never",
			profile: &anime.AutoSelectProfile{
				BatchPreference: anime.AutoSelectPreferenceNever,
			},
			expected: []string{t1.Name, t2.Name, t3.Name, t4.Name, t6.Name, t7.Name, t8.Name},
		},
		{
			name: "Complex combination",
			profile: &anime.AutoSelectProfile{
				MinSeeders:              100,
				RequireCodec:            true,
				PreferredCodecs:         []string{"HEVC"},
				MultipleAudioPreference: anime.AutoSelectPreferenceOnly,
			},
			expected: []string{t3.Name}, // t3 matches all (200 seeders, HEVC, Dual Audio)
			// t6 matches seeders and codec but NOT Dual Audio
			// t5 matches seeders and Dual Audio but NOT Codec (in name)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := s.filter(torrents, tt.profile)

			var filteredNames []string
			for _, ft := range filtered {
				filteredNames = append(filteredNames, ft.Name)
			}

			assert.ElementsMatchf(t, tt.expected, filteredNames, "Expected %v,\ngot %v", tt.expected, filteredNames)
		})
	}
}

func TestAutoSelect_Sort(t *testing.T) {
	s := newTestAutoSelect()

	t1 := &hibiketorrent.AnimeTorrent{Name: "[A] Show - 01 [1080p].mkv", Seeders: 100, Provider: "catsound"}
	t2 := &hibiketorrent.AnimeTorrent{Name: "[B] Show - 01 [720p].mkv", Seeders: 200, Provider: "catsound"}
	t3 := &hibiketorrent.AnimeTorrent{Name: "[C] Show - 01 [1080p] [Dual-Audio].mkv", Seeders: 50, Provider: "tosho"}
	t4 := &hibiketorrent.AnimeTorrent{Name: "[D] Show - 01 [1080p] [HEVC].mkv", Seeders: 80, Provider: "catsound"}

	torrents := []*hibiketorrent.AnimeTorrent{t1, t2, t3, t4}

	tests := []struct {
		name     string
		profile  *anime.AutoSelectProfile
		expected []string // Names in expected order
	}{
		{
			name: "Prefer 1080p",
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			// 1080p torrents get +100 score. 720p gets 0.
			// t1(1080p, 100s), t3(1080p, 50s), t4(1080p, 80s) have score 100.
			// Tie breaker is seeders: t1(100), t4(80), t3(50).
			// t2(720p) comes last.
			expected: []string{t1.Name, t4.Name, t3.Name, t2.Name},
		},
		{
			name: "Prefer Dual Audio",
			profile: &anime.AutoSelectProfile{
				MultipleAudioPreference: anime.AutoSelectPreferencePrefer,
			},
			// t3 has Dual Audio -> +15 score. Others 0.
			expected: []string{t3.Name, t2.Name, t1.Name, t4.Name}, // t3 first. Others sorted by seeders (t2=200, t1=100, t4=80)
		},
		{
			name: "Prefer Provider Animetosho",
			profile: &anime.AutoSelectProfile{
				Providers: []string{"tosho"},
			},
			// t3 matches provider -> +50 score.
			expected: []string{t3.Name, t2.Name, t1.Name, t4.Name},
		},
		{
			name: "Complex Priorities",
			profile: &anime.AutoSelectProfile{
				Resolutions:             []string{"1080p"},                // +100
				PreferredCodecs:         []string{"HEVC"},                 // +40
				MultipleAudioPreference: anime.AutoSelectPreferencePrefer, // +15
			},
			// t1: 1080p (+100) = 100
			// t2: 720p (0) = 0
			// t3: 1080p (+100) + Dual Audio (+15) = 115
			// t4: 1080p (+100) + HEVC (+40) = 140
			// Expected order: t4 (140), t3 (115), t1 (100), t2 (0)
			expected: []string{t4.Name, t3.Name, t1.Name, t2.Name},
		},
		{
			name: "Avoid Dual Audio",
			profile: &anime.AutoSelectProfile{
				MultipleAudioPreference: anime.AutoSelectPreferenceAvoid, // -15
			},
			// t3: -15
			// Others: 0
			// Sorted by seeders for 0 score: t2(200), t1(100), t4(80)
			// Then t3 last.
			expected: []string{t2.Name, t1.Name, t4.Name, t3.Name},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTorrents := make([]*hibiketorrent.AnimeTorrent, len(torrents))
			copy(testTorrents, torrents)

			s.sort(testTorrents, tt.profile)

			var sortedNames []string
			for _, st := range testTorrents {
				sortedNames = append(sortedNames, st.Name)
			}

			assert.Equal(t, tt.expected, sortedNames)
		})
	}
}

func TestAutoSelect_Filter_SourceTokenDoesNotMatchInsideWord(t *testing.T) {
	s := newTestAutoSelect()

	torrents := []*hibiketorrent.AnimeTorrent{
		{Name: "Rooster Fighter - 01 A Rooster Among Cranes 1080p WEB-DL.mkv", Seeders: 50},
	}

	filtered := s.filter(torrents, &anime.AutoSelectProfile{
		RequireSource:    true,
		PreferredSources: []string{"CR"},
	})

	assert.Empty(t, filtered)
}

func TestAutoSelect_Sort_OrderedPreferencesBeatSoftBonuses(t *testing.T) {
	s := newTestAutoSelect()

	preferred := &hibiketorrent.AnimeTorrent{
		Name:     "[ToonsHub] Show - 01 1080p CR WEB-DL AAC2.0 H.264 (Multi-Subs).mkv",
		Seeders:  50,
		Provider: "catnoise",
	}
	lowerPriorityButBonusHeavy := &hibiketorrent.AnimeTorrent{
		Name:          "Show - 01 1080p DSNP WEB-DL DUAL AAC2.0 H.264-VARYG (Dual-Audio, Multi-Subs).mkv",
		Seeders:       100,
		Provider:      "catnoise",
		IsBestRelease: true,
	}

	torrents := []*hibiketorrent.AnimeTorrent{lowerPriorityButBonusHeavy, preferred}
	profile := &anime.AutoSelectProfile{
		Providers:             []string{"catnoise"},
		ReleaseGroups:         []string{"ToonsHub", "VARYG"},
		Resolutions:           []string{"1080p"},
		PreferredCodecs:       []string{"AVC, x264, H.264, H264, H 264"},
		PreferredSources:      []string{"CR", "DSNP"},
		BestReleasePreference: anime.AutoSelectPreferencePrefer,
	}

	s.sort(torrents, profile)

	assert.Equal(t, []string{preferred.Name, lowerPriorityButBonusHeavy.Name}, []string{torrents[0].Name, torrents[1].Name})
}

func TestAutoSelect_Sort_StrongerPrimaryMatchCanBeatHigherReleaseGroup(t *testing.T) {
	s := newTestAutoSelect()

	higherReleaseGroupButLowerQuality := &hibiketorrent.AnimeTorrent{
		Name:     "[ToonsHub] Show - 01 720p CR WEB-DL AAC2.0 H.264.mkv",
		Seeders:  80,
		Provider: "catnoise",
	}
	lowerReleaseGroupButHigherResolution := &hibiketorrent.AnimeTorrent{
		Name:     "Show - 01 1080p DSNP WEB-DL AAC2.0 H.264-VARYG.mkv",
		Seeders:  40,
		Provider: "catnoise",
	}

	torrents := []*hibiketorrent.AnimeTorrent{higherReleaseGroupButLowerQuality, lowerReleaseGroupButHigherResolution}
	profile := &anime.AutoSelectProfile{
		ReleaseGroups:    []string{"ToonsHub", "VARYG"},
		Resolutions:      []string{"1080p"},
		PreferredCodecs:  []string{"AVC, x264, H.264, H264, H 264"},
		PreferredSources: []string{"CR", "DSNP"},
	}

	s.sort(torrents, profile)

	assert.Equal(t, []string{lowerReleaseGroupButHigherResolution.Name, higherReleaseGroupButLowerQuality.Name}, []string{torrents[0].Name, torrents[1].Name})
}

func TestAutoSelect_SmartCachedPrioritization(t *testing.T) {
	s := newTestAutoSelect()

	// these tests prioritization when the provider doesn't support smart search to exclude resolutions

	highQuality1080p := &hibiketorrent.AnimeTorrent{
		Name:     "[SubsPlease] Show - 01 [1080p][HEVC].mkv",
		InfoHash: "hash1",
		Seeders:  200,
		Provider: "catsound",
	}
	mediumQuality1080p := &hibiketorrent.AnimeTorrent{
		Name:     "[RandomGroup] Show - 01 [1080p].mkv",
		InfoHash: "hash2",
		Seeders:  100,
		Provider: "catsound",
	}
	lowQuality720p := &hibiketorrent.AnimeTorrent{
		Name:     "[LowQuality] Show - 01 [720p].mkv",
		InfoHash: "hash3",
		Seeders:  50,
		Provider: "catsound",
	}
	veryLowQuality480p := &hibiketorrent.AnimeTorrent{
		Name:     "[BadGroup] Show - 01 [480p].mkv",
		InfoHash: "hash4",
		Seeders:  10,
		Provider: "catsound",
	}
	highQuality1080pAlt := &hibiketorrent.AnimeTorrent{
		Name:     "[Erai-raws] Show - 01 [1080p][Multiple Subtitle].mkv",
		InfoHash: "hash5",
		Seeders:  150,
		Provider: "tosho",
	}

	tests := []struct {
		name          string
		torrents      []*hibiketorrent.AnimeTorrent
		cachedHashes  []string // Hashes of cached torrents
		profile       *anime.AutoSelectProfile
		expectedOrder []string // Expected names in order
	}{
		{
			name:         "High quality cached should be prioritized",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{"hash1"}, // highQuality1080p is cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{highQuality1080p.Name, mediumQuality1080p.Name, lowQuality720p.Name, veryLowQuality480p.Name},
		},
		{
			// Cached-first within the same audio tier: the cached 480p comes first, then the
			// uncached ones by format. (Cache outranks format, per "cached should always be first".)
			name:         "Cached comes first within the same audio tier",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{"hash4"}, // veryLowQuality480p is cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{veryLowQuality480p.Name, highQuality1080p.Name, mediumQuality1080p.Name, lowQuality720p.Name},
		},
		{
			name:         "Medium quality cached within threshold should be prioritized",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{"hash2"}, // mediumQuality1080p is cached (similar score to high quality)
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{mediumQuality1080p.Name, highQuality1080p.Name, lowQuality720p.Name, veryLowQuality480p.Name},
		},
		{
			name:         "Multiple cached torrents should maintain quality order",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p, highQuality1080pAlt},
			cachedHashes: []string{"hash1", "hash5"}, // Two high quality cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
				Providers:   []string{"tosho"},
			},
			expectedOrder: []string{highQuality1080pAlt.Name, highQuality1080p.Name, mediumQuality1080p.Name, lowQuality720p.Name, veryLowQuality480p.Name},
		},
		{
			// Both cached ones come first (ordered by format), then the uncached ones (by format).
			name:         "Mixed cached: cached first, then by format",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{"hash1", "hash4"}, // High and very low quality cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{highQuality1080p.Name, veryLowQuality480p.Name, mediumQuality1080p.Name, lowQuality720p.Name},
		},
		{
			name:         "When all cached, maintain quality-based order",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{"hash1", "hash2", "hash3", "hash4"}, // All cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{highQuality1080p.Name, mediumQuality1080p.Name, lowQuality720p.Name, veryLowQuality480p.Name},
		},
		{
			name:         "No cached, maintain normal sort order",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, mediumQuality1080p, lowQuality720p, veryLowQuality480p},
			cachedHashes: []string{}, // None cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"},
			},
			expectedOrder: []string{highQuality1080p.Name, mediumQuality1080p.Name, lowQuality720p.Name, veryLowQuality480p.Name},
		},
		{
			name:         "Cached 720p within threshold vs uncached 1080p",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, lowQuality720p},
			cachedHashes: []string{"hash3"}, // 720p is cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p", "720p"}, // Both acceptable
			},
			// When both resolutions are in profile, 720p gets score too and may be within 70% threshold
			// So cached 720p CAN be prioritized if within threshold
			expectedOrder: []string{lowQuality720p.Name, highQuality1080p.Name},
		},
		{
			// Cache outranks format within the same audio tier, so the cached 480p comes first.
			name:         "Cached 480p beats uncached 1080p (same audio tier)",
			torrents:     []*hibiketorrent.AnimeTorrent{highQuality1080p, veryLowQuality480p},
			cachedHashes: []string{"hash4"}, // 480p is cached
			profile: &anime.AutoSelectProfile{
				Resolutions: []string{"1080p"}, // Only 1080p preferred
			},
			expectedOrder: []string{veryLowQuality480p.Name, highQuality1080p.Name},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
				result := make([]*TorrentWithCacheStatus, 0, len(torrents))
				cachedMap := make(map[string]bool)
				for _, hash := range tt.cachedHashes {
					cachedMap[hash] = true
				}

				for _, t := range torrents {
					result = append(result, &TorrentWithCacheStatus{
						Torrent:  t,
						IsCached: cachedMap[t.InfoHash],
					})
				}
				return result
			}

			// Run filterAndSort with postSearchSort
			testTorrents := make([]*hibiketorrent.AnimeTorrent, len(tt.torrents))
			copy(testTorrents, tt.torrents)

			sorted := s.filterAndSort(testTorrents, tt.profile, -1, 0, postSearchSort)

			var sortedNames []string
			for _, st := range sorted {
				sortedNames = append(sortedNames, st.Name)
			}

			assert.Equal(t, tt.expectedOrder, sortedNames)
		})
	}
}

func TestAutoSelect_SmartCachedPrioritization_EdgeCases(t *testing.T) {
	s := newTestAutoSelect()

	t.Run("Empty torrents list", func(t *testing.T) {
		postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
			return []*TorrentWithCacheStatus{}
		}

		result := s.filterAndSort([]*hibiketorrent.AnimeTorrent{}, nil, -1, 0, postSearchSort)
		assert.Empty(t, result)
	})

	t.Run("Single cached torrent", func(t *testing.T) {
		torrent := &hibiketorrent.AnimeTorrent{
			Name:     "[Test] Show - 01 [1080p].mkv",
			InfoHash: "hash1",
			Seeders:  100,
		}

		postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
			return []*TorrentWithCacheStatus{{Torrent: torrents[0], IsCached: true}}
		}

		result := s.filterAndSort([]*hibiketorrent.AnimeTorrent{torrent}, nil, -1, 0, postSearchSort)
		assert.Len(t, result, 1)
		assert.Equal(t, torrent.Name, result[0].Name)
	})

	t.Run("Nil postSearchSort function", func(t *testing.T) {
		torrent := &hibiketorrent.AnimeTorrent{
			Name:     "[Test] Show - 01 [1080p].mkv",
			InfoHash: "hash1",
			Seeders:  100,
		}

		result := s.filterAndSort([]*hibiketorrent.AnimeTorrent{torrent}, nil, -1, 0, nil)
		assert.Len(t, result, 1)
		assert.Equal(t, torrent.Name, result[0].Name)
	})

	t.Run("Cached torrent exactly at 70% threshold", func(t *testing.T) {
		// Create scenario where cached torrent is exactly at threshold
		highQuality := &hibiketorrent.AnimeTorrent{
			Name:     "[SubsPlease] Show - 01 [1080p][HEVC].mkv",
			InfoHash: "hash1",
			Seeders:  200,
			Provider: "tosho",
		}
		thresholdQuality := &hibiketorrent.AnimeTorrent{
			Name:     "[Test] Show - 01 [720p].mkv",
			InfoHash: "hash2",
			Seeders:  50,
		}

		profile := &anime.AutoSelectProfile{
			Resolutions: []string{"1080p"},
			Providers:   []string{"tosho"},
		}

		postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
			return []*TorrentWithCacheStatus{
				{Torrent: torrents[0], IsCached: false},
				{Torrent: torrents[1], IsCached: true},
			}
		}

		result := s.filterAndSort([]*hibiketorrent.AnimeTorrent{highQuality, thresholdQuality}, profile, -1, 0, postSearchSort)
		assert.Len(t, result, 2)
		assert.NotNil(t, result[0])
	})
}

func TestAutoSelect_LanguageDemotion(t *testing.T) {
	s := newTestAutoSelect()

	// Profile mirrors the user's config: English preferred, then Japanese.
	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, eng, english", "jp, jpn, japanese"},
	}

	// Languages expressed as flag emoji (as aggregators do — habari can't read them).
	eng := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] 🌐 🇬🇧", InfoHash: "eng", Seeders: 10}
	jpru := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] 🌐 🇯🇵 🇷🇺", InfoHash: "jpru", Seeders: 20}
	ruOnly := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] 🌐 🇷🇺", InfoHash: "ru", Seeders: 500}

	// ru-only is the only cached one and has by far the most seeders, but it's in the foreign
	// audio tier, which sits below the English/Japanese tiers — so cache (within-tier) can't
	// float it above them. jp/ru has the JP original and stays in the middle tier.
	postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
		out := make([]*TorrentWithCacheStatus, 0, len(torrents))
		for _, tr := range torrents {
			out = append(out, &TorrentWithCacheStatus{Torrent: tr, IsCached: tr.InfoHash == "ru"})
		}
		return out
	}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{ruOnly, jpru, eng}, profile, -1, 0, postSearchSort)

	names := make([]string, len(sorted))
	for i, r := range sorted {
		names[i] = r.Name
	}
	assert.Equal(t, []string{eng.Name, jpru.Name, ruOnly.Name}, names,
		"ru-only must be demoted below preferred-language releases even when cached; eng outranks jp/ru")
}

func TestAutoSelect_LanguageTiers(t *testing.T) {
	s := newTestAutoSelect()
	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, eng, english", "jp, jpn, japanese"},
	}

	// English dub tier (top): en-only, jp/en, and dual-audio (English dub assumed when no foreign
	// flag). Japanese/neutral tier (below): jp-only, jp/ru. Seeders only break ties within a tier,
	// so the jp releases get the most seeders to prove the audio tier wins over seeders.
	enOnly := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] [English].mkv", InfoHash: "en", Seeders: 1}
	jpEn := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] [Japanese] [English].mkv", InfoHash: "jpen", Seeders: 2}
	dual := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] [Dual Audio].mkv", InfoHash: "dual", Seeders: 3}
	jpOnly := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] [Japanese].mkv", InfoHash: "jp", Seeders: 900}
	jpRu := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - 01 [1080p] [Japanese] [Russian].mkv", InfoHash: "jpru", Seeders: 901}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{jpOnly, jpRu, dual, jpEn, enOnly}, profile, -1, 0, nil)
	pos := map[string]int{}
	for i, r := range sorted {
		pos[r.InfoHash] = i
	}
	// Top three (any order) are the English-dub tier; jp-only / jp/ru are below.
	top3 := map[string]bool{sorted[0].InfoHash: true, sorted[1].InfoHash: true, sorted[2].InfoHash: true}
	assert.True(t, top3["en"] && top3["jpen"] && top3["dual"],
		"en-only, jp/en, and dual-audio form the English-dub tier; got %v/%v/%v", sorted[0].Name, sorted[1].Name, sorted[2].Name)
	assert.Greater(t, pos["jp"], pos["dual"], "jp-only ranks below the English-dub tier despite more seeders")
	assert.Greater(t, pos["jpru"], pos["dual"], "jp/ru ranks below the English-dub tier")
}

func TestAutoSelect_FlagLanguages(t *testing.T) {
	s := newTestAutoSelect()
	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, eng, english", "jp, jpn, japanese"},
	}

	// Aggregator names carry language only as flag emoji. An EN flag → en tier (top). A dual/fr
	// release is jp/fr (dual implies the JP original), so it sits in the JP tier — NOT demoted.
	// A SINGLE french release (no dual) has no preferred language and is demoted to the bottom,
	// even with the most seeders.
	enFlag := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] 🌐 🇬🇧 / 🇯🇵", InfoHash: "en", Seeders: 1}
	jpFlag := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] 🌐 🇯🇵", InfoHash: "jp", Seeders: 5}
	frDual := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] Dual Audio 🌐 🇫🇷", InfoHash: "frdual", Seeders: 50}
	frSingle := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] 🌐 🇫🇷", InfoHash: "fr", Seeders: 900}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{frSingle, frDual, jpFlag, enFlag}, profile, -1, 10, nil)
	pos := map[string]int{}
	for i, r := range sorted {
		pos[r.InfoHash] = i
	}
	assert.Equal(t, "en", sorted[0].InfoHash, "EN-flag must be top")
	assert.Equal(t, "fr", sorted[len(sorted)-1].InfoHash, "single french demoted last despite most seeders")
	assert.Less(t, pos["frdual"], pos["fr"], "dual/fr (jp/fr) ranks above single french")
}

func TestAutoSelect_SizeUnitNotLanguage(t *testing.T) {
	s := newTestAutoSelect()
	// "gb" is in the preferred list (Great Britain → English). The "GB" in a gigabyte size must
	// NOT count as English, otherwise every GB-sized release falsely ranks in the en tier.
	profile := &anime.AutoSelectProfile{
		Resolutions:        []string{"1080p"},
		PreferredLanguages: []string{"en, gb, eng, english", "jp, jpn, japanese"},
	}
	esBigGB := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] WEBRip 2.32 GB 🌐 🇪🇸", InfoHash: "es", Seeders: 900}
	enFlag := &hibiketorrent.AnimeTorrent{Name: "Show S01 E10 [1080p] WEBRip 531 MB 🌐 🇬🇧", InfoHash: "en", Seeders: 1}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{esBigGB, enFlag}, profile, -1, 10, nil)
	assert.Equal(t, "en", sorted[0].InfoHash, "actual EN flag must outrank a GB-sized Spanish release")
	assert.Equal(t, "es", sorted[1].InfoHash, "the GB size must not make the Spanish release English")
}

func TestAutoSelect_SizeTiebreak(t *testing.T) {
	s := newTestAutoSelect()
	profile := &anime.AutoSelectProfile{
		PreferredLanguages:      []string{"en, eng, english", "jp, jpn, japanese"},
		MultipleAudioPreference: anime.AutoSelectPreferencePrefer,
	}
	// Two equal-tier English dubs (dual audio, same resolution, neither matches a preferred codec
	// or source). The higher-bitrate (larger) one wins the tie.
	small := &hibiketorrent.AnimeTorrent{Name: "[Lat] Show S01 E10 [1080p] WEB-DL Dual Audio", InfoHash: "small", Seeders: 0, Size: 651 * 1024 * 1024}
	big := &hibiketorrent.AnimeTorrent{Name: "[ToonsHub] Show S01 E10 [1080p] WEB-DL AVC AAC Dual Audio", InfoHash: "big", Seeders: 0, Size: 1490 * 1024 * 1024}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{small, big}, profile, -1, 10, nil)
	assert.Equal(t, "big", sorted[0].InfoHash, "larger (higher-bitrate) release wins the tie")
}

func TestAutoSelect_EpisodeRelevance(t *testing.T) {
	s := newTestAutoSelect()
	profile := &anime.AutoSelectProfile{Resolutions: []string{"1080p"}}

	// Requesting episode 10. The cached E01-07 batch and a single E02 don't contain it and
	// must sink; the real E10 file and a full-season batch (no episode in name) stay on top.
	ep10 := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show - S01E10 [1080p].mkv", InfoHash: "e10", Seeders: 20}
	fullBatch := &hibiketorrent.AnimeTorrent{Name: "[Grp] Show S01 [1080p] Batch.mkv", InfoHash: "fb", Seeders: 10, IsBatch: true}
	wrongBatch := &hibiketorrent.AnimeTorrent{Name: "Show (2026) S01 E01-07 1080p WEBRip HEVC-Rutor", InfoHash: "wb", Seeders: 999}
	wrongSingle := &hibiketorrent.AnimeTorrent{Name: "Show (2026) S01E02 1080p WEBRip", InfoHash: "ws", Seeders: 999}

	// The wrong-episode ones are cached and have the most seeders — without the episode gate,
	// cache-first would float them to the top.
	postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
		out := make([]*TorrentWithCacheStatus, 0, len(torrents))
		for _, tr := range torrents {
			out = append(out, &TorrentWithCacheStatus{Torrent: tr, IsCached: tr.InfoHash == "wb" || tr.InfoHash == "ws"})
		}
		return out
	}

	sorted := s.filterAndSort([]*hibiketorrent.AnimeTorrent{wrongBatch, wrongSingle, ep10, fullBatch}, profile, -1, 10, postSearchSort)
	names := make([]string, len(sorted))
	for i, r := range sorted {
		names[i] = r.Name
	}
	// Last two must be the wrong-episode releases (order between them not asserted).
	last2 := map[string]bool{names[len(names)-1]: true, names[len(names)-2]: true}
	assert.True(t, last2[wrongBatch.Name] && last2[wrongSingle.Name],
		"wrong-episode releases must be buried at the bottom even when cached; got order %v", names)
	assert.NotEqual(t, wrongBatch.Name, names[0])
}

func TestAutoSelect_SeasonGate(t *testing.T) {
	s := newTestAutoSelect()

	s1 := &hibiketorrent.AnimeTorrent{Name: "[Group] Wistoria Wand and Sword S1 - 01 [1080p].mkv", InfoHash: "s1", Seeders: 500}
	s2 := &hibiketorrent.AnimeTorrent{Name: "[Group] Wistoria Wand and Sword S2 - 01 [1080p].mkv", InfoHash: "s2", Seeders: 50}
	noSeason := &hibiketorrent.AnimeTorrent{Name: "[Group] Wistoria Wand and Sword - 01 [1080p].mkv", InfoHash: "ns", Seeders: 80}
	combined := &hibiketorrent.AnimeTorrent{Name: "[Group] Wistoria Wand and Sword S1-S2 Batch [1080p].mkv", InfoHash: "cb", Seeders: 30, IsBatch: true}

	torrents := []*hibiketorrent.AnimeTorrent{s1, s2, noSeason, combined}
	profile := &anime.AutoSelectProfile{Resolutions: []string{"1080p"}}

	// expectedSeason = 2: the S1-only release must be dropped; S2 / season-less / combined kept.
	result := s.filterAndSort(torrents, profile, 2, 0, nil)

	names := make([]string, len(result))
	for i, r := range result {
		names[i] = r.Name
	}
	assert.NotContains(t, names, s1.Name, "S1 release should be dropped for a S2 request")
	assert.Contains(t, names, s2.Name)
	assert.Contains(t, names, noSeason.Name, "season-less release should pass the gate")
	assert.Contains(t, names, combined.Name, "combined S1-S2 batch should pass the gate")
	// Despite far fewer seeders, the declared-S2 release should outrank the season-less one.
	assert.Equal(t, s2.Name, names[0], "declared-correct season should score highest")
}

// Roman-numeral / word season labels that habari doesn't parse must still be recognized (via
// comparison.ExtractSeasonNumber), so the correct sequel episode wins and a leaking S1 batch /
// wrong-season roman release is buried — in the debrid Rank path too, where there is no gate and
// the S1 batch is cached. This is the Classroom-of-the-Elite-IV / DanMachi-IV report.
func TestAutoSelect_SeasonMismatch_RomanAndUnlabeledBatch(t *testing.T) {
	s := newTestAutoSelect()
	profile := &anime.AutoSelectProfile{Resolutions: []string{"1080p"}, BatchPreference: anime.AutoSelectPreferencePrefer}

	correct := &hibiketorrent.AnimeTorrent{Name: "[Grp] DanMachi IV - 10 [1080p].mkv", InfoHash: "ok", Seeders: 20}
	s1batch := &hibiketorrent.AnimeTorrent{Name: "[Grp] DanMachi [Batch] [1080p].mkv", InfoHash: "s1b", Seeders: 9000, IsBatch: true}
	wrongRoman := &hibiketorrent.AnimeTorrent{Name: "[Grp] DanMachi III - 10 [1080p].mkv", InfoHash: "s3", Seeders: 9000}

	// expectedSeason = 4, requesting episode 10. The S1 batch is cached with huge seeders — under
	// the old logic (cache-first, no season recognition) it would float to the top.
	postSearchSort := func(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
		out := make([]*TorrentWithCacheStatus, 0, len(torrents))
		for _, tr := range torrents {
			out = append(out, &TorrentWithCacheStatus{Torrent: tr, IsCached: tr.InfoHash == "s1b"})
		}
		return out
	}

	// Rank = the debrid path (no season gate; only scoring + cache prioritization).
	ranked := s.Rank([]*hibiketorrent.AnimeTorrent{s1batch, wrongRoman, correct}, profile, 4, 10, postSearchSort)
	assert.Equal(t, correct.Name, ranked[0].Name, "correct-season episode must win over a cached S1 batch and a wrong-season roman release")
	// A declared-wrong season (roman "III") is a hard mismatch (bottom band); the unlabeled S1 batch
	// is only suspected, so it sinks below the correct episode but stays above the hard mismatch.
	assert.Equal(t, wrongRoman.Name, ranked[len(ranked)-1].Name, "the declared-wrong-season release must sink to the very bottom")

	// filterAndSort = the auto-download path: the wrong-season roman release is now gated out entirely.
	filtered := s.filterAndSort([]*hibiketorrent.AnimeTorrent{s1batch, wrongRoman, correct}, profile, 4, 10, nil)
	names := make([]string, len(filtered))
	for i, r := range filtered {
		names[i] = r.Name
	}
	assert.Equal(t, correct.Name, names[0], "correct-season episode ranks first")
	assert.NotContains(t, names, wrongRoman.Name, "wrong-season roman release should be gated out")
}
