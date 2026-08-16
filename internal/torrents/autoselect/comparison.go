package autoselect

import (
	"cmp"
	"context"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"seanime/internal/util/comparison"
	"regexp"
	"slices"
	"strings"

	"github.com/5rahim/habari"
)

// trustedSourceRe matches releases that come straight from the retail source rather than from a
// third-party re-encode of it: Crunchyroll (the aggregator's "📡 Crunchyroll" service tag, or the
// scene abbreviation "CR") and disc sources.
//
// Crunchyroll is here for its SUBTITLES, not its video. Its official subs typeset signs, translate
// song lyrics and keep terminology consistent across a season; the small HEVC re-encodes that
// dominate the results for a currently-airing show almost always strip that work down to plain
// dialogue. That difference is invisible to every other signal we rank on — the re-encode parses as
// the same 1080p, the same episode, the same audio, and wins on a preferred codec.
//
// Disc sources share the rung rather than sitting below it so a BluRay/REMUX is never demoted by a
// CR WEB-DL: within the rung the existing score decides, and it already weights BluRay (+30 source)
// and REMUX (+100) above a web release.
var trustedSourceRe = regexp.MustCompile(`(?i)\b(crunchyroll|cr|blu[-. ]?ray|bd[-. ]?rip|bdmv|remux)\b`)

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
	// A release that carries the Japanese original ALONGSIDE a non-preferred language — a LoliHouse
	// "🌐 🇯🇵 / 🇨🇳" WEBRip (Japanese audio, Chinese subtitles) or a jp/ru dub. Sized to dominate the
	// whole format spread (~450 at most: resolution 100 + group 50 + codec 40 + source 30 + the
	// bonuses) so it always sinks below a plain Japanese release, yet stay well inside the neutral
	// band (|score| < 1000) so it never falls below a foreign-ONLY release, which has no playable
	// Japanese track at all.
	scoreForeignMarketRelease = 700

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
	audioLangs      []string // languages that actually describe the AUDIO (see deriveAudioLanguages)
	nameLangMatchOK bool     // safe to look for a language as free text in the name
	isDualAudio     bool     // declares a dual-audio track set (JP original + English dub)
	// isServiceMultiAudio marks a multi-audio release from a service that always ships the
	// English dub — the one way to tell a real dub from the French scene "MULTi".
	isServiceMultiAudio bool
	expectedSeason  int      // Expected season of the requested media (>=2 for sequels), 0/-1 = unknown
	expectedEpisode int      // Requested episode number, <=0 = unknown (skip episode scoring)
	mediaYear       int      // Requested media's start year, 0 = unknown (skip year scoring)
	// seasonExact is true when the release explicitly declares the requested (sequel) season.
	// It is a SORT KEY rather than a score, because a score can always be out-weighed by format
	// bonuses: an S1 BluRay REMUX beat the correctly-labelled S03 WEB-DL of Mushoku Tensei
	// (+100 remux +30 BluRay source vs +60 season match) and played a season-1 file.
	seasonExact bool
	// trustedSource is true for a release taken straight from the retail source (Crunchyroll, or a
	// disc) instead of re-encoded from it — see trustedSourceRe. Like seasonExact it is a SORT KEY,
	// not a score: the whole problem is that a re-encode wins on format weights (a preferred codec
	// is +40) while carrying visibly worse subtitles, so any score-level signal is out-weighed by
	// the thing it is meant to beat.
	trustedSource bool
	priority      int
	bonus         int
	score         int
}

type TorrentWithCacheStatus struct {
	Torrent  *hibiketorrent.AnimeTorrent
	IsCached bool
}

// filterAndSort filters and sorts the torrents based on the profile or defaults.
func (s *AutoSelect) filterAndSort(
	ctx context.Context,
	torrents []*hibiketorrent.AnimeTorrent,
	profile *anime.AutoSelectProfile,
	expectedSeason int,
	expectedEpisode int,
	mediaYear int,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
) []*hibiketorrent.AnimeTorrent {
	s.log("Filtering and sorting torrents")
	s.logger.Debug().Int("count", len(torrents)).Msg("autoselect: Filtering and sorting torrents")

	if len(torrents) == 0 {
		return torrents
	}

	// Optimize: Parse metadata once
	candidates := buildCandidates(torrents, expectedSeason, expectedEpisode, mediaYear)

	// Filter
	candidates = s.filterCandidates(candidates, profile)

	// Sort by profile scores first
	s.sortCandidates(candidates, profile)

	filteredTorrents := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		filteredTorrents[i] = c.torrent
	}
	// Always run the same final ladder. Without a prioritizer nothing is cached, but the
	// resolution floor and the curated-best rung still apply — the torrent-stream path used to
	// skip both and rank on the raw score alone.
	filteredTorrents = s.smartCachedPrioritization(filteredTorrents, candidates, profile, postSearchSort)

	// Populate the candidates list for the status updates
	scoreOf := make(map[*hibiketorrent.AnimeTorrent]int, len(candidates))
	for _, c := range candidates {
		scoreOf[c.torrent] = c.score
	}
	candidatesList := make([]AutoSelectCandidate, len(filteredTorrents))
	for i, t := range filteredTorrents {
		score := scoreOf[t]
		candidatesList[i] = AutoSelectCandidate{
			Name:     t.Name,
			Provider: t.Provider,
			Seeders:  t.Seeders,
			Score:    score,
			Status:   "waiting",
		}
	}
	s.updateCandidates(ctx, candidatesList)

	return filteredTorrents
}

// buildCandidates parses metadata once for each torrent.
func buildCandidates(torrents []*hibiketorrent.AnimeTorrent, expectedSeason int, expectedEpisode int, mediaYear int) []*candidate {
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
			mediaYear:       mediaYear,
		}
		c := candidates[i]
		c.isDualAudio = isDualAudioRelease(c.parsed, c.lowerName)
		c.isServiceMultiAudio = util.IsServiceMultiAudio(c.parsed.AudioTerm, c.lowerName)
		c.audioLangs, c.nameLangMatchOK = deriveAudioLanguages(c.parsed, c.flagLanguages, c.isDualAudio)
		c.trustedSource = trustedSourceRe.MatchString(c.lowerName)
	}
	return candidates
}

// isDualAudioRelease reports whether a release declares a dual-audio track set. "dual audio" is
// the fansub convention for the Japanese original PLUS an English dub, so it is credible evidence
// of English audio on its own — unlike "multi", see the note on dualAudioNameRe.
func isDualAudioRelease(parsed *habari.Metadata, lowerName string) bool {
	return util.IsDualAudioRelease(parsed.AudioTerm, lowerName)
}

// deriveAudioLanguages adapts habari's metadata to util.DeriveAudioLanguages, which is the single
// audio-vs-subtitle rule shared with the torrent-list badges — see its doc comment.
func deriveAudioLanguages(parsed *habari.Metadata, flagLangs []string, isDualAudio bool) (langs []string, nameMatchOK bool) {
	return util.DeriveAudioLanguages(flagLangs, parsed.Language, parsed.Subtitles, isDualAudio)
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

// seasonCovered reports whether the requested season is covered by a release's declared seasons.
// For a multi-season pack, habari parses a span like "S1 - S4" as just the endpoints {1,4}
// (it drops the dash), so an exact-membership test wrongly rejects the mid-range seasons 2 and 3.
// Treat any pack that declares 2+ seasons as covering the inclusive range [min..max]; single
// episodes still require an exact match.
func seasonCovered(seasons []int, expected int, isPack bool) bool {
	if slices.Contains(seasons, expected) {
		return true
	}
	if isPack && len(seasons) >= 2 {
		lo, hi := seasons[0], seasons[0]
		for _, n := range seasons {
			if n < lo {
				lo = n
			}
			if n > hi {
				hi = n
			}
		}
		return expected >= lo && expected <= hi
	}
	return false
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
	mediaYear int,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
) []*hibiketorrent.AnimeTorrent {
	if len(torrents) == 0 {
		return torrents
	}

	candidates := buildCandidates(torrents, expectedSeason, expectedEpisode, mediaYear)
	s.sortCandidates(candidates, profile)

	sorted := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		sorted[i] = c.torrent
	}
	return s.smartCachedPrioritization(sorted, candidates, profile, postSearchSort)
}

// filter is a shim for testing or legacy usage.
func (s *AutoSelect) filter(torrents []*hibiketorrent.AnimeTorrent, profile *anime.AutoSelectProfile) []*hibiketorrent.AnimeTorrent {
	candidates := s.filterCandidates(buildCandidates(torrents, 0, 0, 0), profile)
	ret := make([]*hibiketorrent.AnimeTorrent, len(candidates))
	for i, c := range candidates {
		ret[i] = c.torrent
	}
	return ret
}

// sort is a shim for testing or legacy usage.
func (s *AutoSelect) sort(torrents []*hibiketorrent.AnimeTorrent, profile *anime.AutoSelectProfile) {
	candidates := buildCandidates(torrents, 0, 0, 0)
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
//     tag/flag/name, OR a dual-audio release with no foreign dub flag (dual = JP original + a dub
//     assumed to be the top preference unless a flag names a different one).
//   - Japanese original / neutral / dual-with-foreign-dub (e.g. jp/fr): 0.
//   - Foreign-only (a single non-preferred language, no JP original, not dual): big negative.
//
// Every language test reads c.audioLangs, never parsed.Language directly — see
// deriveAudioLanguages for why the raw parse can't be trusted to describe audio.
func audioLanguageScore(c *candidate, profile *anime.AutoSelectProfile) int {
	groups := profile.PreferredLanguages
	if len(groups) == 0 {
		return 0
	}

	matchesGroup := func(groupIdx int) bool {
		if groupIdx < 0 || groupIdx >= len(groups) {
			return false
		}
		for _, lang := range strings.Split(groups[groupIdx], ",") {
			lang = strings.TrimSpace(lang)
			if lang == "" {
				continue
			}
			if slices.ContainsFunc(c.audioLangs, func(pl string) bool { return strings.EqualFold(pl, lang) }) ||
				(c.nameLangMatchOK && containsBoundedTerm(c.lowerName, lang)) {
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

	isDual := c.isDualAudio

	// A declared language that is neither preferred nor the Japanese original marks a release made
	// for another market (FR, RU, CN…). Japanese is excluded explicitly rather than by relying on
	// the preferred groups: a profile that lists only English would otherwise read every Japanese
	// original as foreign and demote the entire result set.
	//
	// The presence of a Japanese track does NOT excuse it. A "🌐 🇯🇵 / 🇨🇳" LoliHouse WEBRip is
	// Japanese audio with CHINESE subtitles — unwatchable here, but it used to land in the neutral
	// band and then win it on a 10-bit bonus, beating the English-subbed releases of the same
	// episode. Whether the foreign language is a dub track or a subtitle track, the release is
	// built for a market that isn't ours.
	hasForeignLang := slices.ContainsFunc(c.audioLangs, func(l string) bool {
		return !tokenInAnyGroup(l) && !isJapaneseToken(l)
	})
	hasJapanese := slices.ContainsFunc(c.audioLangs, isJapaneseToken)

	// English dub: top preferred audio present, a dual with no foreign dub language, or a
	// multi-audio release from a service that always includes the English dub.
	if matchesGroup(0) || (isDual && !hasForeignLang) || (c.isServiceMultiAudio && !hasForeignLang) {
		return scoreEnglishDub
	}
	// Foreign-only: a declared non-preferred language with no JP original and not dual.
	if hasForeignLang && !isDual && !hasJapanese {
		return -scoreForeignAudio
	}
	// Japanese original plus a foreign one (jp/cn, jp/ru): still playable — the JP track is there —
	// so it stays in the neutral band, but it is a release built for another market and its
	// subtitles are in that market's language. Demoted WITHIN the band, below a plain Japanese
	// release, instead of down to the foreign-only band where it would rank below a release that
	// has no Japanese track at all.
	if hasForeignLang && !isDual {
		return -scoreForeignMarketRelease
	}
	// Japanese original / neutral.
	return 0
}

// containsTerm reports whether any parsed term contains any of the given lowercase needles.
func containsTerm(terms []string, needles ...string) bool {
	for _, s := range terms {
		lower := strings.ToLower(s)
		for _, n := range needles {
			if strings.Contains(lower, n) {
				return true
			}
		}
	}
	return false
}

func containsMultiOrDual(terms []string) bool {
	return containsTerm(terms, "multi", "dual", "dub")
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

// filterCandidates applies the profile's constraints. The season gate is the one filter driven by
// inferred data rather than by the user's profile: expectedSeason comes from the entry title or
// the metadata provider, either of which can disagree with how releases are labelled (animap
// defaults an unmapped new cour to season 1). When it is the reason nothing survived, retry
// without it — scoring still buries wrong-season releases below anything correct, which beats
// failing the request outright with "no file found".
func (s *AutoSelect) filterCandidates(candidates []*candidate, profile *anime.AutoSelectProfile) []*candidate {
	filtered, seasonGated := s.filterCandidatesOnce(candidates, profile, true)
	if len(filtered) == 0 && seasonGated > 0 {
		s.logger.Warn().Int("gated", seasonGated).Msg("autoselect: Season gate removed every candidate, retrying without it")
		filtered, _ = s.filterCandidatesOnce(candidates, profile, false)
	}
	return filtered
}

func (s *AutoSelect) filterCandidatesOnce(candidates []*candidate, profile *anime.AutoSelectProfile, applySeasonGate bool) (filtered []*candidate, seasonGated int) {
	if profile == nil {
		return candidates, 0
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

	for _, c := range candidates {
		t := c.torrent
		parsed := c.parsed

		// Season gate (sequels only): drop torrents that explicitly declare seasons,
		// none of which is the requested season. Season-less releases and combined
		// batches that include the requested season pass through.
		if c.expectedSeason >= 2 {
			if seasons := declaredSeasons(c); len(seasons) > 0 && !seasonCovered(seasons, c.expectedSeason, isUnlabeledSeasonPack(c)) {
				seasonGated++
				if applySeasonGate {
					continue
				}
			}
		}

		// Exclude terms. Matched on token boundaries like every other term test here — a bare
		// substring makes "raw" exclude the release group "Erai-raws".
		if len(excludeTerms) > 0 {
			excluded := false
			for _, term := range excludeTerms {
				if containsBoundedTerm(c.lowerName, term) {
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

		// Language requirement. Reads exactly what the ranking path credits as audio
		// (c.audioLangs — flag emoji when present, otherwise habari's languages minus the ones
		// that describe subtitles), plus a bounded name match. Sharing the source keeps the
		// filter from being stricter than the scorer.
		if profile.RequireLanguage && len(preferredLanguages) > 0 {
			foundLang := false
			for _, lang := range preferredLanguages {
				if slices.ContainsFunc(c.audioLangs, func(pl string) bool {
					return strings.EqualFold(pl, lang)
				}) || (c.nameLangMatchOK && containsBoundedTerm(c.lowerName, lang)) {
					foundLang = true
					break
				}
			}
			if !foundLang {
				continue
			}
		}

		// Seeders filtering. 0 and -1 mean "unknown", which is what an aggregator reports for a
		// debrid-backed stream with no swarm — treating that as "fewer than MinSeeders" would
		// drop every result. Only a real, known-small swarm is filtered.
		if profile.MinSeeders > 0 && t.Seeders > 0 && t.Seeders < profile.MinSeeders {
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
	return filtered, seasonGated
}

func (s *AutoSelect) sortCandidates(candidates []*candidate, profile *anime.AutoSelectProfile) {
	for _, c := range candidates {
		c.priority, c.bonus = s.calculateScoreBreakdown(c, profile)
		c.score = c.priority + c.bonus
	}

	slices.SortStableFunc(candidates, func(a, b *candidate) int {
		ba, bb := scoreBand(a.score), scoreBand(b.score)
		// Gate first: a release that can't contain the requested episode, or declares the wrong
		// season, is unusable and sorts last whatever else it has going for it.
		if (ba == bandGated) != (bb == bandGated) {
			return cmp.Compare(bb, ba)
		}
		// Then season-exactness — ABOVE the audio tier and the format score. Playing the wrong
		// cour is a total failure; getting Japanese audio instead of a dub is a preference. This
		// has to be a sort key rather than a score: the format weights (BluRay source + REMUX +
		// codec) out-weigh any season bonus, which is how a season-1 BD REMUX won a season-3
		// request and played "[Lulu] Mushoku Tensei - 05".
		if a.seasonExact != b.seasonExact {
			return boolFirst(a.seasonExact)
		}
		// Audio tier. Band ordering matches priority ordering (the ±2000 / ±100000 terms that
		// define a band all live in priority), so this only inserts the two keys above format.
		if ba != bb {
			return cmp.Compare(bb, ba)
		}

		if a.priority != b.priority {
			return cmp.Compare(b.priority, a.priority)
		}

		if a.bonus != b.bonus {
			return cmp.Compare(b.bonus, a.bonus)
		}

		if a.score != b.score {
			return cmp.Compare(b.score, a.score)
		}

		if tb := sizeTieBreak(a.torrent, b.torrent); tb != 0 {
			return tb
		}
		return cmp.Compare(b.torrent.Seeders, a.torrent.Seeders)
	})
}

// resolutionTier ranks a candidate by video resolution (higher = better). It is the quality
// floor for cache prioritization: cache status may only reorder releases WITHIN the same
// resolution tier, never let a cached low-res release outrank an uncached higher-res one
// (the decreed quality-over-cache invariant). 0 = resolution unknown/other (ranked lowest).
func resolutionTier(c *candidate) int {
	res := ""
	if c.parsed != nil {
		res = strings.ToLower(c.parsed.VideoResolution)
	}
	name := c.lowerName
	has := func(needle string) bool { return strings.Contains(res, needle) || strings.Contains(name, needle) }
	switch {
	case has("2160") || has("4k"):
		return 4
	case has("1080"):
		return 3
	case has("720"):
		return 2
	case has("480") || has("576"):
		return 1
	default:
		return 0
	}
}

// uncachedStatuses is the prioritizer used when the caller has no cache information (the
// torrent-stream path): order is preserved and nothing counts as cached.
func uncachedStatuses(torrents []*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus {
	out := make([]*TorrentWithCacheStatus, 0, len(torrents))
	for _, t := range torrents {
		out = append(out, &TorrentWithCacheStatus{Torrent: t})
	}
	return out
}

// boolFirst orders true before false in a slices.SortStableFunc comparator.
func boolFirst(v bool) int {
	if v {
		return -1
	}
	return 1
}

// isCuratedBestRelease reports whether a release is SeaDex-curated AND not a dead swarm. The
// seeder guard only rejects a genuinely near-dead swarm (1-2 seeders): 0 and -1 mean "unknown",
// which is what aggregators report for debrid-backed streams that have no swarm at all. The old
// `Seeders == -1 || Seeders > 2` form silently disqualified every SeaDex result coming from
// AIOStreams (it reports `seeders ?? 0`), so the curated-best bonus never applied in production.
func isCuratedBestRelease(t *hibiketorrent.AnimeTorrent) bool {
	return t.IsBestRelease && (t.Seeders <= 0 || t.Seeders > 2)
}

// sizeTieBreak orders two releases when every stronger signal is equal. A multi-episode batch's
// total size is NOT a per-episode bitrate signal, so when exactly one side is a batch the single
// episode wins (its size reflects real bitrate); otherwise larger size ≈ higher bitrate. Returns
// <0 if a should rank first, >0 if b, 0 if still tied. Batch PREFERENCE is already folded into
// score upstream, so this only fires on a genuine score tie.
func sizeTieBreak(a, b *hibiketorrent.AnimeTorrent) int {
	if a.IsBatch != b.IsBatch {
		if a.IsBatch {
			return 1
		}
		return -1
	}
	if a.Size != b.Size {
		return cmp.Compare(b.Size, a.Size)
	}
	return 0
}

// smartCachedPrioritization applies the postSearchSort (which identifies cached torrents) and
// orders releases within each audio/episode band by: resolution tier (quality floor) → cache
// status → per-candidate score (English dub → codec/source) → per-episode size → seeders. A
// cached stream plays instantly, so cache wins as a tie-break WITHIN a resolution tier, but it
// never lets a lower-resolution cached release outrank a higher-resolution uncached one.
func (s *AutoSelect) smartCachedPrioritization(
	torrents []*hibiketorrent.AnimeTorrent,
	candidates []*candidate,
	profile *anime.AutoSelectProfile,
	postSearchSort func([]*hibiketorrent.AnimeTorrent) []*TorrentWithCacheStatus,
) []*hibiketorrent.AnimeTorrent {

	if len(torrents) == 0 {
		return torrents
	}
	if postSearchSort == nil {
		postSearchSort = uncachedStatuses
	}

	// Keyed by pointer: InfoHash is empty for the aggregator's infohash-less debrid streams, so a
	// string key collapses all of them onto one candidate and hands the rest score 0.
	candidateMap := make(map[*hibiketorrent.AnimeTorrent]*candidate, len(candidates))
	for _, c := range candidates {
		candidateMap[c.torrent] = c
	}

	type rankItem struct {
		torrent     *hibiketorrent.AnimeTorrent
		score       int
		cached      bool
		resTier     int
		seasonExact bool
		best        bool
		trusted     bool
	}
	items := make([]rankItem, 0, len(torrents))
	for _, tws := range postSearchSort(torrents) {
		it := rankItem{torrent: tws.Torrent, cached: tws.IsCached}
		if c, ok := candidateMap[tws.Torrent]; ok {
			it.score = c.score
			it.resTier = resolutionTier(c)
			it.seasonExact = c.seasonExact
			it.best = isCuratedBestRelease(c.torrent) &&
				(profile == nil || profile.BestReleasePreference != anime.AutoSelectPreferenceAvoid)
			it.trusted = c.trustedSource
		}
		items = append(items, it)
	}

	// Sort lexicographically: audio/episode band → resolution tier (quality floor) → curated →
	// trusted source → cached within the tier → format score → per-episode size → seeders. So order is correct-episode
	// English dub → … → foreign → wrong-episode; within a band a higher resolution always wins,
	// and only within one resolution tier does a cached release come first.
	slices.SortStableFunc(items, func(a, b rankItem) int {
		ba, bb := scoreBand(a.score), scoreBand(b.score)
		// Same ladder as sortCandidates: unusable (wrong episode/season) last, then the right
		// season, then the audio tier. Playing the wrong cour is a worse outcome than the wrong
		// audio language, a lower resolution, or a slower (uncached) start.
		if (ba == bandGated) != (bb == bandGated) {
			return cmp.Compare(bb, ba)
		}
		if a.seasonExact != b.seasonExact {
			return boolFirst(a.seasonExact)
		}
		if ba != bb {
			return cmp.Compare(bb, ba)
		}
		// Quality floor: never let a cached lower-resolution release outrank an uncached
		// higher-resolution one (decreed quality-over-cache invariant).
		if a.resTier != b.resTier {
			return cmp.Compare(b.resTier, a.resTier)
		}
		// A SeaDex-curated release is the best-known encode of the episode, so it also outranks
		// cache — the same quality-over-cache rule, one rung down from resolution. This is what
		// puts SeaDex on top of the Japanese tier for shows with no English dub.
		if a.best != b.best {
			return boolFirst(a.best)
		}
		// Retail source (Crunchyroll subs / disc) beats a re-encode of it, cached or not — the same
		// quality-over-cache rule one rung further down. This is the only rung that sees subtitle
		// quality: for Mushoku Tensei S03E08 every candidate was 1080p and cached, so the ladder
		// fell through to score, where a preferred codec (+40) put a 309 MB HEVC re-encode above the
		// 1.72 GB Crunchyroll WEB-DL it was made from. Disc sources share the rung so a BluRay is
		// never demoted by a web release; the score below still orders BluRay/REMUX above WEB-DL.
		if a.trusted != b.trusted {
			return boolFirst(a.trusted)
		}
		if a.cached != b.cached {
			return boolFirst(a.cached)
		}
		if a.score != b.score {
			return cmp.Compare(b.score, a.score)
		}
		if tb := sizeTieBreak(a.torrent, b.torrent); tb != 0 {
			return tb
		}
		return cmp.Compare(b.torrent.Seeders, a.torrent.Seeders)
	})

	result := make([]*hibiketorrent.AnimeTorrent, 0, len(torrents))
	for i, it := range items {
		result = append(result, it.torrent)
		if i < 3 {
			s.logger.Debug().Str("name", it.torrent.Name).Bool("cached", it.cached).Bool("trusted", it.trusted).Int("score", it.score).Str("provider", it.torrent.Provider).Msg("autoselect: Top candidates")
		}
	}
	return result
}

// RankerVersion fingerprints the ranking rules. Bump it whenever the ordering changes, so caches
// that store "the release auto-select would pick" — the debrid prewarm store — stop serving
// selections computed by an older ladder. Without this a ranking fix only reaches entries that
// happen to miss the cache, which is exactly the continue-watching titles a user is mid-way
// through and would notice first.
const RankerVersion = "2026-08-16"

// bandGated is the band of a release that can't serve the request at all (wrong episode or a
// declared season other than the requested one). Named because the sort ladders treat it
// differently from the audio tiers: it outranks nothing, not even a wrong-season match.
const bandGated = 0

// scoreBand maps a candidate score to its ranking band, given the magnitude layering: episode
// mismatch (-100000) << foreign (-2000) < jp/neutral (~0) < English dub (+2000), with format
// adding at most a few hundred. Higher band = ranked higher.
func scoreBand(score int) int {
	switch {
	case score < -50000:
		return bandGated // wrong episode / wrong season — always last
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

	// Release groups. The name fallback matters for aggregator results: their "name" starts with
	// a debrid tag, so habari reports ReleaseGroup="TB" and the real group (from the 🏷️ segment)
	// only ever appears in the name text — without the fallback this whole weight is dead there.
	if len(profile.ReleaseGroups) > 0 {
		for i, group := range profile.ReleaseGroups {
			if strings.EqualFold(parsed.ReleaseGroup, group) || containsBoundedTerm(c.lowerName, group) {
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
	c.seasonExact = false
	if c.expectedSeason >= 2 {
		seasons := declaredSeasons(c)
		switch {
		case len(seasons) == 0:
			if isUnlabeledSeasonPack(c) {
				priority -= scoreSeasonAmbiguousBatch
			}
		case seasonCovered(seasons, c.expectedSeason, isUnlabeledSeasonPack(c)):
			c.seasonExact = true
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

	// Wrong-cour guard: a release whose enclosed year is far from the entry's start year is a
	// different cour, even when the season label is missing or numbered in a foreign convention
	// (Honzuki "Adopted Daughter" airs 2026; the wrongly-picked "S02" batch is the 2020 Part 2).
	// Convention-free, so it complements the season gate. ±1 tolerance covers post-air BD lag;
	// releases with no parseable year are left untouched.
	//
	// Multi-episode packs are EXEMPT: complete-series batches are commonly labeled with the
	// PREMIERE year ("Show (2019) S1-S4 Complete"), so the guard buried exactly the curated
	// batches the user prefers whenever a later cour was streamed. Wrong-season batches are
	// already policed by the season gate (labeled: the Honzuki 2020 "S02" case) and the
	// ambiguous-batch demotion (unlabeled); the year guard is for mislabeled/foreign-convention
	// SINGLES, where the year is the only cour signal.
	//
	// A release that names the requested season is exempt too — it has already told us the cour
	// directly, and singles carry the premiere year just like batches do: aggregator names such
	// as "Fruits Basket (2019) S03 • E05" parse to Year=2019 with Season=[03], which the guard
	// would otherwise bury for a 2021 entry.
	if c.mediaYear > 0 && parsed.Year != "" && !isUnlabeledSeasonPack(c) && !c.seasonExact {
		if ty, ok := util.StringToInt(parsed.Year); ok && ty > 0 {
			diff := ty - c.mediaYear
			if diff < 0 {
				diff = -diff
			}
			if diff > 1 {
				priority -= scoreSeasonMismatch
			}
		}
	}

	// Best release preference (prefer/avoid)
	isBestRelease := isCuratedBestRelease(t)
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
