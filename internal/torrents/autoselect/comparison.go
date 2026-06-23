package autoselect

import (
	"cmp"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"seanime/internal/util/comparison"
	"slices"
	"strings"

	"github.com/5rahim/habari"
)

const (
	scoreResolutionBase    = 100
	scoreResolutionDecay   = 10
	scoreProviderBase      = 5
	scoreProviderDecay     = 1
	scoreReleaseGroupBase  = 50
	scoreReleaseGroupDecay = 5
	scoreCodecBase         = 40
	scoreCodecDecay        = 5
	scoreSourceBase        = 30
	scoreSourceDecay       = 5
	scoreLanguageBase      = 20
	scoreLanguageDecay     = 2
	// Penalty for releases that declare language(s), none of which is preferred
	// (e.g. Russian-only). Sized ~= resolution weight so they sink below every
	// preferred-language release and below the cached-first quality threshold.
	scoreLanguageUnpreferred = 100
	scoreMultiAudio        = 15
	scoreMultiSubs         = 10
	scoreBatch             = 20
	scoreBestRelease       = 20
	scoreSeasonMatch       = 60
	// A release that declares a season other than the requested one (e.g. an S1 batch labelled
	// "III" for an S4 request). Priority-level so it sinks to the bottom band even when cached —
	// the debrid path (Rank) doesn't run the season gate, so scoring is the only thing that buries it.
	scoreSeasonMismatch = 100000
	// A sequel (S2+) request matched a full-season pack that declares NO season anywhere — almost
	// always the S1 batch leaking in via a base-title synonym. Demote below correctly-matched
	// single episodes (one band), but softer than a declared mismatch since it's only suspected.
	scoreSeasonAmbiguousBatch = 3000
	// Ranking is layered by magnitude so the order is: correct episode → audio tier → (cache,
	// applied separately) → format. Episode mismatch dominates the audio tiers; the audio tiers
	// dominate format (max ~350). Goal: the top result is the highest-quality cached English dub.
	scoreEpisodeMismatch = 100000 // wrong episode → always last, even cached
	scoreEnglishDub      = 2000    // English (top-preferred) audio / dub present
	scoreForeignAudio    = 2000    // a release only in a non-preferred foreign language

	// Quality-signal tiebreakers, borrowed from AIOStreams' visualTag/audioTag/seadex sort
	// keys. Deliberately SMALL (sum well under 1000) so they only separate otherwise-equivalent
	// releases WITHIN a band — they can never lift a non-English release above an English dub
	// (audioLanguageScore is ±2000, applied in `priority`) nor cross the episode/season gates.
	scoreBitDepth10    = 12 // 10-bit encode (better gradients, standard for modern anime)
	scoreHDR           = 8  // HDR10 / Dolby Vision
	scoreLosslessAudio = 10 // FLAC only — lossless AND decodable by the native (Chromium) player
	scoreSeadexBest    = 30 // SeaDex-curated "best" release
	// REMUX is the top within-English preference (untouched disc — often the uncensored/extended
	// cut with restored content). Sized to dominate the other quality bonuses *combined* (~75) so
	// an English REMUX beats any non-REMUX English release, yet far below the 2000 dub band so it
	// can never lift a non-English REMUX above an English release. Playability caveat: REMUXes
	// often carry DTS-HD/TrueHD/PCM the native Chromium player can't decode — see
	// remux-audio-support.md / task #32 (use mpv / Tenji meanwhile).
	scoreRemux = 100
)

type candidate struct {
	torrent        *hibiketorrent.AnimeTorrent
	parsed         *habari.Metadata
	lowerName       string
	flagLanguages   []string // languages decoded from flag emoji in the raw name (aggregators)
	expectedSeason  int      // Expected season of the requested media (>=2 for sequels), 0/-1 = unknown
	expectedEpisode int      // Requested episode number, <=0 = unknown (skip episode scoring)
	priority        int
	bonus          int
	score          int
}

type TorrentWithCacheStatus struct {
	Torrent  *hibiketorrent.AnimeTorrent
	IsCached bool
}

// filterAndSort filters and sorts the torrents based on the profile or defaults.
func (s *AutoSelect) filterAndSort(torrents []*hibiketorrent.AnimeTorrent, profile *anime.AutoSelectProfile, expectedSeason int, expectedEpisode int, postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus) []*hibiketorrent.AnimeTorrent {
	s.log("Filtering and sorting torrents")
	s.logger.Debug().Int("count", len(torrents)).Msg("autoselect: Filtering and sorting torrents")

	if len(torrents) == 0 {
		return torrents
	}

	// Optimize: Parse metadata once
	candidates := buildCandidates(torrents, expectedSeason, expectedEpisode)

	// Filter
	candidates = s.filterCandidates(candidates, profile)

	// Sort by profile scores first
	s.sortCandidates(candidates, profile)

	// apply torrent prioritization if provided
	if postSearchSort != nil {
		filteredTorrents := make([]*hibiketorrent.AnimeTorrent, len(candidates))
		for i, c := range candidates {
			filteredTorrents[i] = c.torrent
		}
		return s.smartCachedPrioritization(filteredTorrents, candidates, profile, postSearchSort)
	}

	filteredTorrents := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		if i < 3 {
			s.logger.Debug().Str("name", c.torrent.Name).Int("seeders", c.torrent.Seeders).Int("score", c.score).Str("provider", c.torrent.Provider).Msg("autoselect: Top selection")
		}
		filteredTorrents[i] = c.torrent
	}

	return filteredTorrents
}

// buildCandidates parses metadata once for each torrent.
func buildCandidates(torrents []*hibiketorrent.AnimeTorrent, expectedSeason int, expectedEpisode int) []*candidate {
	candidates := make([]*candidate, len(torrents))
	for i, t := range torrents {
		candidates[i] = &candidate{
			torrent: t,
			parsed:  habari.Parse(util.CleanReleaseName(t.Name)),
			// Use the cleaned name (size tokens + emoji stripped) for term matching so a size
			// unit can't be read as a language — e.g. the "GB" in "2.32 GB" matching a preferred
			// "gb" (Great Britain → English). Flags are decoded from the raw name separately.
			lowerName:       strings.ToLower(util.CleanReleaseName(t.Name)),
			flagLanguages:   util.LanguagesFromFlags(t.Name),
			expectedSeason:  expectedSeason,
			expectedEpisode: expectedEpisode,
		}
	}
	return candidates
}

// episodeCovered reports whether a parsed episode range can contain the requested episode.
// Empty parsed episodes (full-season batches, movies, or unnumbered releases) are treated as
// covered so we never bury a valid batch; only releases that clearly declare a different
// episode/range are flagged. Uses min..max of the parsed numbers (handles "E01-07" ranges).
func episodeCovered(parsedEpisodes []string, requested int) bool {
	if requested <= 0 || len(parsedEpisodes) == 0 {
		return true
	}
	lo, hi := -1, -1
	for _, e := range parsedEpisodes {
		if n, ok := util.StringToInt(e); ok {
			if lo == -1 || n < lo {
				lo = n
			}
			if hi == -1 || n > hi {
				hi = n
			}
		}
	}
	if lo == -1 { // unparseable -> don't penalize
		return true
	}
	return requested >= lo && requested <= hi
}

// declaredSeasons returns the season numbers a release name declares. It prefers habari's parse
// (which holds ranges like "S1-S2" as multiple values) and falls back to the richer
// comparison.ExtractSeasonNumber when habari finds nothing — habari misses roman numerals
// ("Classroom of the Elite IV"), Japanese "期", and bare/word ordinals that ExtractSeasonNumber
// catches. Empty result = no season declared at all.
func declaredSeasons(c *candidate) []int {
	var out []int
	for _, sn := range c.parsed.SeasonNumber {
		if n, ok := util.StringToInt(sn); ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		if n := comparison.ExtractSeasonNumber(c.torrent.Name); n >= 1 {
			out = append(out, n)
		}
	}
	return out
}

// isUnlabeledSeasonPack reports whether a candidate is a multi-episode / full-season pack rather
// than a single episode. Used (for sequels with no declared season) to demote the leaking S1 batch
// while leaving season-less single episodes — which match the requested relative episode — alone.
func isUnlabeledSeasonPack(c *candidate) bool {
	return c.torrent.IsBatch || len(c.parsed.EpisodeNumber) != 1
}

// Rank orders torrents using the same scoring (profile + season match) and cache
// prioritization as auto-select, but WITHOUT dropping any. It backs the manual selection
// screen, so the list mirrors what auto-select would pick while still showing every result.
func (s *AutoSelect) Rank(
	torrents []*hibiketorrent.AnimeTorrent,
	profile *anime.AutoSelectProfile,
	expectedSeason int,
	expectedEpisode int,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
) []*hibiketorrent.AnimeTorrent {
	if len(torrents) == 0 {
		return torrents
	}

	candidates := buildCandidates(torrents, expectedSeason, expectedEpisode)
	s.sortCandidates(candidates, profile)

	sorted := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		sorted[i] = c.torrent
	}

	if postSearchSort != nil {
		return s.smartCachedPrioritization(sorted, candidates, profile, postSearchSort)
	}
	return sorted
}

// filter is a shim for testing or legacy usage.
func (s *AutoSelect) filter(torrents []*hibiketorrent.AnimeTorrent, profile *anime.AutoSelectProfile) []*hibiketorrent.AnimeTorrent {
	candidates := make([]*candidate, len(torrents))
	for i, t := range torrents {
		candidates[i] = &candidate{
			torrent:   t,
			parsed:    habari.Parse(util.CleanReleaseName(t.Name)),
			lowerName: strings.ToLower(t.Name),
		}
	}
	candidates = s.filterCandidates(candidates, profile)
	ret := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		ret[i] = c.torrent
	}
	return ret
}

// sort is a shim for testing or legacy usage.
func (s *AutoSelect) sort(torrents []*hibiketorrent.AnimeTorrent, profile *anime.AutoSelectProfile) {
	candidates := make([]*candidate, len(torrents))
	for i, t := range torrents {
		candidates[i] = &candidate{
			torrent:   t,
			parsed:    habari.Parse(util.CleanReleaseName(t.Name)),
			lowerName: strings.ToLower(t.Name),
		}
	}
	s.sortCandidates(candidates, profile)
	for i, c := range candidates {
		torrents[i] = c.torrent
	}
}

// isJapaneseToken reports whether a preferred-language token refers to Japanese. Used to treat
// dual/multi-audio releases as containing the Japanese original (anime's source language).
func isJapaneseToken(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "jp", "jpn", "ja", "japanese":
		return true
	}
	return false
}

// audioLanguageScore classifies a candidate's audio into three tiers and returns a score that
// dominates format scoring. The goal is "highest-quality English dub on top":
//   - English dub (top-preferred audio): big positive. Matches the top preferred language via a
//     tag/flag/name, OR a dual/multi-audio release with no foreign dub flag (dual = JP original
//     + a dub assumed to be the top preference unless a flag names a different one).
//   - Japanese original / neutral / dual-with-foreign-dub (e.g. jp/fr): 0.
//   - Foreign-only (a single non-preferred language, no JP original, not dual): big negative.
func audioLanguageScore(c *candidate, profile *anime.AutoSelectProfile) int {
	groups := profile.PreferredLanguages
	if len(groups) == 0 {
		return 0
	}
	parsed := c.parsed

	matchesGroup := func(groupIdx int) bool {
		if groupIdx < 0 || groupIdx >= len(groups) {
			return false
		}
		for _, lang := range strings.Split(groups[groupIdx], ",") {
			lang = strings.TrimSpace(lang)
			if lang == "" {
				continue
			}
			if slices.ContainsFunc(parsed.Language, func(pl string) bool { return strings.EqualFold(pl, lang) }) ||
				slices.ContainsFunc(c.flagLanguages, func(fl string) bool { return strings.EqualFold(fl, lang) }) ||
				containsBoundedTerm(c.lowerName, lang) {
				return true
			}
		}
		return false
	}

	tokenInAnyGroup := func(tok string) bool {
		for gi := range groups {
			for _, lang := range strings.Split(groups[gi], ",") {
				if strings.EqualFold(strings.TrimSpace(lang), tok) {
					return true
				}
			}
		}
		return false
	}

	isDual := containsMultiOrDual(parsed.AudioTerm) || containsMultiOrDual([]string{c.lowerName})

	// A declared language (flag emoji or parsed tag) that isn't in any preferred group is a
	// foreign dub (e.g. FR, RU).
	hasForeignLang := false
	for _, fl := range c.flagLanguages {
		if !tokenInAnyGroup(fl) {
			hasForeignLang = true
			break
		}
	}
	if !hasForeignLang {
		for _, pl := range parsed.Language {
			if !tokenInAnyGroup(pl) {
				hasForeignLang = true
				break
			}
		}
	}
	hasJapanese := slices.ContainsFunc(c.flagLanguages, isJapaneseToken) ||
		slices.ContainsFunc(parsed.Language, isJapaneseToken)

	// English dub: top preferred audio present, or a dual with no foreign dub language.
	if matchesGroup(0) || (isDual && !hasForeignLang) {
		return scoreEnglishDub
	}
	// Foreign-only: a declared non-preferred language with no JP original and not dual.
	if hasForeignLang && !isDual && !hasJapanese {
		return -scoreForeignAudio
	}
	// Japanese original / neutral / dual-with-foreign (jp/fr).
	return 0
}

func containsMultiOrDual(terms []string) bool {
	for _, s := range terms {
		lower := strings.ToLower(s)
		if strings.Contains(lower, "multi") || strings.Contains(lower, "dual") || strings.Contains(lower, "dub") {
			return true
		}
	}
	return false
}

func splitAndClean(items []string) []string {
	var ret []string
	for _, item := range items {
		for _, sub := range strings.Split(item, ",") {
			ret = append(ret, strings.TrimSpace(sub))
		}
	}
	return ret
}

func checkPreference(condition bool, preference anime.AutoSelectPreference) bool {
	if preference == anime.AutoSelectPreferenceOnly && !condition {
		return false
	}
	if preference == anime.AutoSelectPreferenceNever && condition {
		return false
	}
	return true
}

func isTokenChar(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
}

func containsBoundedTerm(lowerValue string, term string) bool {
	lowerTerm := strings.ToLower(strings.TrimSpace(term))
	if lowerTerm == "" {
		return false
	}

	searchFrom := 0
	for {
		idx := strings.Index(lowerValue[searchFrom:], lowerTerm)
		if idx == -1 {
			return false
		}

		idx += searchFrom
		end := idx + len(lowerTerm)

		leftBoundary := idx == 0 || !isTokenChar(lowerValue[idx-1])
		rightBoundary := end == len(lowerValue) || !isTokenChar(lowerValue[end])
		if leftBoundary && rightBoundary {
			return true
		}

		searchFrom = idx + 1
	}
}

func (s *AutoSelect) filterCandidates(candidates []*candidate, profile *anime.AutoSelectProfile) []*candidate {
	if profile == nil {
		return candidates
	}

	// Pre-process profile constraints
	var excludeTerms []string
	for _, term := range profile.ExcludeTerms {
		excludeTerms = append(excludeTerms, strings.ToLower(term))
	}

	preferredLanguages := splitAndClean(profile.PreferredLanguages)
	preferredCodecs := splitAndClean(profile.PreferredCodecs)
	preferredSources := splitAndClean(profile.PreferredSources)

	// Parse sizes
	var minSize int64 = -1
	if profile.MinSize != "" {
		if val, err := util.StringToBytes(profile.MinSize); err == nil {
			minSize = val
		}
	}
	var maxSize int64 = -1
	if profile.MaxSize != "" {
		if val, err := util.StringToBytes(profile.MaxSize); err == nil {
			maxSize = val
		}
	}

	var filtered []*candidate
	for _, c := range candidates {
		t := c.torrent
		parsed := c.parsed

		// Season gate (sequels only): drop torrents that explicitly declare seasons,
		// none of which is the requested season. Season-less releases and combined
		// batches that include the requested season pass through.
		if c.expectedSeason >= 2 {
			if seasons := declaredSeasons(c); len(seasons) > 0 && !slices.Contains(seasons, c.expectedSeason) {
				continue
			}
		}

		// Exclude terms
		if len(excludeTerms) > 0 {
			excluded := false
			for _, term := range excludeTerms {
				if strings.Contains(c.lowerName, term) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}

		// bad
		if profile.BestReleasePreference == anime.AutoSelectPreferenceOnly && !t.IsBestRelease {
			continue
		}

		if profile.BestReleasePreference == anime.AutoSelectPreferenceNever && t.IsBestRelease {
			continue
		}

		// Language requirement
		if profile.RequireLanguage && len(preferredLanguages) > 0 {
			foundLang := false
			// Check parsed language
			if len(parsed.Language) > 0 {
				for _, lang := range preferredLanguages {
					if slices.ContainsFunc(parsed.Language, func(pl string) bool {
						return strings.EqualFold(pl, lang)
					}) {
						foundLang = true
						break
					}
				}
			} else { // Fallback to string matching
				for _, lang := range preferredLanguages {
					if len(lang) > 3 && containsBoundedTerm(c.lowerName, lang) {
						foundLang = true
						break
					}
				}
			}
			if !foundLang {
				continue
			}
		}

		// Seeders filtering
		if profile.MinSeeders > 0 && t.Seeders < profile.MinSeeders {
			continue
		}

		// Size filtering
		if minSize != -1 && t.Size > 0 && t.Size < minSize {
			continue
		}
		if maxSize != -1 && t.Size > 0 && t.Size > maxSize {
			continue
		}

		// Require codec
		if profile.RequireCodec && len(preferredCodecs) > 0 {
			foundCodec := false
			for _, codec := range preferredCodecs {
				if slices.ContainsFunc(parsed.VideoTerm, func(vt string) bool {
					return strings.EqualFold(vt, codec)
				}) {
					foundCodec = true
					break
				}
				if containsBoundedTerm(c.lowerName, codec) {
					foundCodec = true
					break
				}
			}
			if !foundCodec {
				continue
			}
		}

		// Require source
		if profile.RequireSource && len(preferredSources) > 0 {
			foundSource := false
			for _, source := range preferredSources {
				if slices.ContainsFunc(parsed.Source, func(src string) bool {
					return strings.EqualFold(src, source)
				}) {
					foundSource = true
					break
				}
				if containsBoundedTerm(c.lowerName, source) {
					foundSource = true
					break
				}
			}
			if !foundSource {
				continue
			}
		}

		// Preferences
		if !checkPreference(containsMultiOrDual(parsed.AudioTerm), profile.MultipleAudioPreference) {
			continue
		}
		if !checkPreference(containsMultiOrDual(parsed.Subtitles), profile.MultipleSubsPreference) {
			continue
		}
		if !checkPreference(t.IsBatch, profile.BatchPreference) {
			continue
		}

		filtered = append(filtered, c)
	}
	return filtered
}

func (s *AutoSelect) sortCandidates(candidates []*candidate, profile *anime.AutoSelectProfile) {
	for _, c := range candidates {
		c.priority, c.bonus = s.calculateScoreBreakdown(c, profile)
		c.score = c.priority + c.bonus
	}

	slices.SortStableFunc(candidates, func(a, b *candidate) int {
		if a.priority != b.priority {
			return cmp.Compare(b.priority, a.priority)
		}

		if a.bonus != b.bonus {
			return cmp.Compare(b.bonus, a.bonus)
		}

		if a.score != b.score {
			return cmp.Compare(b.score, a.score)
		}

		// Tie-break by size (higher bitrate ≈ better quality at the same resolution), then seeders.
		if a.torrent.Size != b.torrent.Size {
			return cmp.Compare(b.torrent.Size, a.torrent.Size)
		}
		return cmp.Compare(b.torrent.Seeders, a.torrent.Seeders)
	})
}

// smartCachedPrioritization applies the postSearchSort (which identifies cached torrents) and
// puts ALL cached torrents before all uncached ones, preserving the existing score order within
// each group. Cache is the outermost sort key (cached first), then the per-candidate score
// (English dub → format quality). For debrid streaming a cached stream plays instantly, so it
// always wins; the score order then surfaces the best English dub within each cache group.
func (s *AutoSelect) smartCachedPrioritization(
	torrents []*hibiketorrent.AnimeTorrent,
	candidates []*candidate,
	_ *anime.AutoSelectProfile,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
) []*hibiketorrent.AnimeTorrent {

	if len(torrents) == 0 {
		return torrents
	}

	candidateMap := make(map[string]*candidate, len(candidates))
	for _, c := range candidates {
		candidateMap[c.torrent.InfoHash] = c
	}

	type rankItem struct {
		torrent *hibiketorrent.AnimeTorrent
		score   int
		cached  bool
	}
	items := make([]rankItem, 0, len(torrents))
	for _, tws := range postSearchSort(torrents) {
		score := 0
		if c, ok := candidateMap[tws.Torrent.InfoHash]; ok {
			score = c.score
		}
		items = append(items, rankItem{torrent: tws.Torrent, score: score, cached: tws.IsCached})
	}

	// Sort lexicographically: audio/episode band (from the score magnitude) → cached within the
	// band → format score → seeders. So order is correct-episode English dub → … → foreign →
	// wrong-episode, and within each band the cached releases come first, then by format.
	slices.SortStableFunc(items, func(a, b rankItem) int {
		if ba, bb := scoreBand(a.score), scoreBand(b.score); ba != bb {
			return cmp.Compare(bb, ba)
		}
		if a.cached != b.cached {
			if a.cached {
				return -1
			}
			return 1
		}
		if a.score != b.score {
			return cmp.Compare(b.score, a.score)
		}
		// Tie-break by size (higher bitrate ≈ better quality at the same resolution), then seeders.
		if a.torrent.Size != b.torrent.Size {
			return cmp.Compare(b.torrent.Size, a.torrent.Size)
		}
		return cmp.Compare(b.torrent.Seeders, a.torrent.Seeders)
	})

	result := make([]*hibiketorrent.AnimeTorrent, 0, len(torrents))
	for i, it := range items {
		result = append(result, it.torrent)
		if i < 3 {
			s.logger.Debug().Str("name", it.torrent.Name).Bool("cached", it.cached).Int("score", it.score).Str("provider", it.torrent.Provider).Msg("autoselect: Top candidates")
		}
	}
	return result
}

// scoreBand maps a candidate score to its ranking band, given the magnitude layering: episode
// mismatch (-100000) << foreign (-2000) < jp/neutral (~0) < English dub (+2000), with format
// adding at most a few hundred. Higher band = ranked higher.
func scoreBand(score int) int {
	switch {
	case score < -50000:
		return 0 // wrong episode (or otherwise excluded) — always last
	case score <= -1000:
		return 1 // foreign-only audio
	case score >= 1000:
		return 3 // English dub
	default:
		return 2 // Japanese original / neutral
	}
}

func (s *AutoSelect) calculateScore(c *candidate, profile *anime.AutoSelectProfile) int {
	priority, bonus := s.calculateScoreBreakdown(c, profile)
	return priority + bonus
}

func (s *AutoSelect) calculateScoreBreakdown(c *candidate, profile *anime.AutoSelectProfile) (priority int, bonus int) {
	parsed := c.parsed
	t := c.torrent

	if profile == nil {
		return 0, 0
	}

	// Resolution. Fall back to a bounded name match when habari can't parse the resolution
	// (it misses some aggregator/formatter name layouts, e.g. "SeaDex 1080p (Best)" → ""),
	// mirroring how codec/source below already name-match. Without this, a release whose
	// resolution doesn't parse silently loses the full resolution weight and sinks below an
	// equivalent release that did parse — even though both are the same resolution.
	if len(profile.Resolutions) > 0 {
		for i, res := range profile.Resolutions {
			if strings.EqualFold(parsed.VideoResolution, res) || containsBoundedTerm(c.lowerName, strings.ToLower(res)) {
				priority += scoreResolutionBase - (i * scoreResolutionDecay)
				break
			}
		}
	}

	// Providers
	if len(profile.Providers) > 0 {
		for i, provider := range profile.Providers {
			if strings.EqualFold(t.Provider, provider) {
				priority += scoreProviderBase - (i * scoreProviderDecay)
				break
			}
		}
	}

	// Release groups
	if len(profile.ReleaseGroups) > 0 {
		for i, group := range profile.ReleaseGroups {
			if strings.EqualFold(parsed.ReleaseGroup, group) {
				priority += scoreReleaseGroupBase - (i * scoreReleaseGroupDecay)
				break
			}
		}
	}

	// Codec
	if len(profile.PreferredCodecs) > 0 {
		for i, codecs := range profile.PreferredCodecs {
			matched := false
			for _, codec := range strings.Split(codecs, ",") {
				codec = strings.TrimSpace(codec)
				if slices.ContainsFunc(parsed.VideoTerm, func(vt string) bool {
					return strings.EqualFold(vt, codec)
				}) || slices.ContainsFunc(parsed.AudioTerm, func(at string) bool {
					return strings.EqualFold(at, codec)
				}) {
					priority += scoreCodecBase - (i * scoreCodecDecay)
					matched = true
					break
				}
				if containsBoundedTerm(c.lowerName, codec) {
					priority += scoreCodecBase - (i * scoreCodecDecay)
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	// Source
	if len(profile.PreferredSources) > 0 {
		for i, sources := range profile.PreferredSources {
			matched := false
			for _, source := range strings.Split(sources, ",") {
				source = strings.TrimSpace(source)
				if slices.ContainsFunc(parsed.Source, func(src string) bool {
					return strings.EqualFold(src, source)
				}) {
					priority += scoreSourceBase - (i * scoreSourceDecay)
					matched = true
					break
				}
				if containsBoundedTerm(c.lowerName, source) {
					priority += scoreSourceBase - (i * scoreSourceDecay)
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	// Audio language: English dub on top, foreign-only demoted (dominates format below).
	priority += audioLanguageScore(c, profile)

	// Multiple audio preference (prefer/avoid)
	isMultiAudio := containsMultiOrDual(parsed.AudioTerm)
	if profile.MultipleAudioPreference == anime.AutoSelectPreferencePrefer && isMultiAudio {
		bonus += scoreMultiAudio
	}
	if profile.MultipleAudioPreference == anime.AutoSelectPreferenceAvoid && isMultiAudio {
		bonus -= scoreMultiAudio
	}

	// Multiple subs preference (prefer/avoid)
	isMultiSubs := containsMultiOrDual(parsed.Subtitles)
	if profile.MultipleSubsPreference == anime.AutoSelectPreferencePrefer && isMultiSubs {
		bonus += scoreMultiSubs
	}
	if profile.MultipleSubsPreference == anime.AutoSelectPreferenceAvoid && isMultiSubs {
		bonus -= scoreMultiSubs
	}

	// Batch preference (prefer/avoid)
	isBatch := t.IsBatch
	if profile.BatchPreference == anime.AutoSelectPreferencePrefer && isBatch {
		bonus += scoreBatch
	}
	if profile.BatchPreference == anime.AutoSelectPreferenceAvoid && isBatch {
		bonus -= scoreBatch
	}

	// Season relevance (sequels only). Three cases:
	//   - declares the requested season  -> reward.
	//   - declares a different season     -> sink to the bottom band (the gate drops these in the
	//     auto-download path, but the debrid Rank path doesn't gate, so scoring must bury them).
	//   - declares no season at all       -> a full-season pack here is almost always the S1 batch
	//     leaking in via a base-title synonym; demote it below correctly-matched singles. Season-less
	//     single episodes are left alone (they match the requested relative episode).
	if c.expectedSeason >= 2 {
		seasons := declaredSeasons(c)
		switch {
		case len(seasons) == 0:
			if isUnlabeledSeasonPack(c) {
				priority -= scoreSeasonAmbiguousBatch
			}
		case slices.Contains(seasons, c.expectedSeason):
			bonus += scoreSeasonMatch
		default:
			priority -= scoreSeasonMismatch
		}
	}

	// Episode relevance: bury results whose declared episodes can't include the requested one
	// (e.g. an E01-07 batch for an episode-10 request). Full-season batches / unnumbered
	// releases have no parsed episodes and are left untouched.
	if c.expectedEpisode > 0 && !episodeCovered(parsed.EpisodeNumber, c.expectedEpisode) {
		priority -= scoreEpisodeMismatch
	}

	// Best release preference (prefer/avoid)
	isBestRelease := t.IsBestRelease && (t.Seeders == -1 || t.Seeders > 2)
	if profile.BestReleasePreference == anime.AutoSelectPreferencePrefer && isBestRelease {
		bonus += scoreBestRelease
	}
	if profile.BestReleasePreference == anime.AutoSelectPreferenceAvoid && isBestRelease {
		bonus -= scoreBestRelease
	}

	// Quality-signal tiebreakers (AIOStreams-inspired). BONUS only, so they rank quality WITHIN
	// a band without ever overriding the English-dub priority — an English/dual-audio source is
	// still guaranteed to be picked over a Japanese-only one when it exists. FLAC is the only
	// lossless audio rewarded: it signals quality AND decodes in the native (Chromium) player,
	// unlike DTS-HD/TrueHD/PCM which would play silently there.
	name := c.lowerName
	if containsBoundedTerm(name, "10bit") || containsBoundedTerm(name, "10-bit") ||
		containsBoundedTerm(name, "hi10") || containsBoundedTerm(name, "hi10p") {
		bonus += scoreBitDepth10
	}
	if containsBoundedTerm(name, "hdr") || containsBoundedTerm(name, "hdr10") ||
		containsBoundedTerm(name, "dovi") || strings.Contains(name, "dolby vision") {
		bonus += scoreHDR
	}
	if containsBoundedTerm(name, "flac") {
		bonus += scoreLosslessAudio
	}
	if isBestRelease && profile.BestReleasePreference != anime.AutoSelectPreferenceAvoid {
		bonus += scoreSeadexBest
	}
	if containsBoundedTerm(name, "remux") {
		bonus += scoreRemux
	}

	return priority, bonus
}
