package util

import (
	"regexp"
	"strings"
)

// sizeTokenRe matches file-size tokens like "833 MB", "950MB", "4.94 GB", "227.2 MiB".
// Units are restricted to KB/MB/GB/TB (binary or decimal) so it never touches resolutions
// ("1080p"), bit-depth ("10bit"), codecs ("x265"), or years.
var sizeTokenRe = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s*[KMGT]i?B\b`)

// noiseRe matches emoji, pictographs, symbols, variation selectors, and bullets. Aggregator
// providers (Debridio via AIOStreams) pack these — plus newlines — into the torrent "name".
var noiseRe = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}\x{2022}]`)

// wsRe collapses runs of whitespace (including the newlines aggregator names embed).
var wsRe = regexp.MustCompile(`\s+`)

// StripSizeTokens removes file-size tokens from a release name so parsers don't mistake the
// size value for an episode number (e.g. "Witch Hat Atelier (2026) 833 MB" → episode 833).
func StripSizeTokens(name string) string {
	return sizeTokenRe.ReplaceAllString(name, " ")
}

// CleanReleaseName normalizes a messy release/torrent name before parsing: it removes emoji
// and pictographs, strips file-size tokens, and collapses newlines/whitespace to single
// spaces. Without this, aggregator names like
//
//	"Debridio Scraper 1080p\n📁 Witch Hat Atelier (2026) S01 • E12\n📦 833 MB ..."
//
// make habari read the size as the episode and drop the real episode/season (the newlines
// split the name so each line parses on its own).
func CleanReleaseName(name string) string {
	name = noiseRe.ReplaceAllString(name, " ")
	name = sizeTokenRe.ReplaceAllString(name, " ")
	name = wsRe.ReplaceAllString(name, " ")
	return strings.TrimSpace(name)
}

// flagCountryToLangTokens maps ISO country codes (from flag emoji) to language tokens that a
// preferred-languages list might use. Aggregator providers (AIOStreams) encode a release's
// languages as flag emoji in the name (🇬🇧 🇯🇵 🇫🇷), which name parsers can't read.
var flagCountryToLangTokens = map[string][]string{
	"GB": {"en", "eng", "english"}, "US": {"en", "eng", "english"}, "AU": {"en", "eng", "english"},
	"CA": {"en", "eng", "english"}, "NZ": {"en", "eng", "english"}, "IE": {"en", "eng", "english"},
	"JP": {"jp", "jpn", "ja", "japanese"},
	"FR": {"fr", "fre", "fra", "french"},
	"ES": {"es", "spa", "spanish"}, "MX": {"es", "spa", "spanish"}, "AR": {"es", "spa", "spanish"},
	"RU": {"ru", "rus", "russian"},
	"DE": {"de", "ger", "deu", "german"}, "AT": {"de", "ger", "deu", "german"},
	"IT": {"it", "ita", "italian"},
	"BR": {"pt", "por", "portuguese", "brazilian"}, "PT": {"pt", "por", "portuguese"},
	"CN": {"zh", "chi", "zho", "chinese"}, "TW": {"zh", "chi", "zho", "chinese"}, "HK": {"zh", "chi", "zho", "chinese"},
	"KR": {"ko", "kor", "korean"},
}

// subtitleFlagMarker (📝) separates AUDIO languages from SUBTITLE languages inside an
// aggregator (AIOStreams) language line:
//
//	🌐 🇬🇧 / 🇯🇵📝 🇬🇧 / 🇸🇦 / 🇪🇸 / 🇫🇷 …
//
// i.e. "audio: English, Japanese — subtitles: English, Arabic, Spanish, French". Everything
// after the marker is subtitles, so scanning the whole name reads a Japanese-audio release with
// English subs as an English dub (and the UI badges it "Dubbed").
const subtitleFlagMarker = "\U0001F4DD"

// audioFlagSegment returns the part of a release name that can declare AUDIO languages: the
// text before the first subtitle marker. Names without the marker are returned unchanged.
func audioFlagSegment(name string) string {
	if i := strings.Index(name, subtitleFlagMarker); i != -1 {
		return name[:i]
	}
	return name
}

// LanguagesFromFlags decodes flag emoji (regional-indicator pairs) in a release name into
// language tokens, so language scoring can see languages that are only expressed as flags.
// Only the AUDIO segment is scanned (see subtitleFlagMarker) — subtitle flags must never be
// credited as audio. Unknown countries fall back to their lowercase code so they still count
// as a declared (non-preferred) language. Returns deduplicated lowercase tokens.
func LanguagesFromFlags(name string) []string {
	runes := []rune(audioFlagSegment(name))
	seen := make(map[string]bool)
	var out []string
	add := func(toks []string) {
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if a >= 0x1F1E6 && a <= 0x1F1FF && b >= 0x1F1E6 && b <= 0x1F1FF {
			cc := string(rune('A'+(a-0x1F1E6))) + string(rune('A'+(b-0x1F1E6)))
			if toks, ok := flagCountryToLangTokens[cc]; ok {
				add(toks)
			} else {
				add([]string{strings.ToLower(cc)})
			}
			i++ // consume the second indicator of the pair
		}
	}
	return out
}

// flagDisplayName maps a country code (from a flag emoji) to a single human-readable language
// name, for surfacing in the UI. Mirrors flagCountryToLangTokens but collapses each to one label.
var flagDisplayName = map[string]string{
	"GB": "English", "US": "English", "AU": "English", "CA": "English", "NZ": "English", "IE": "English",
	"JP": "Japanese", "FR": "French", "ES": "Spanish", "MX": "Spanish", "AR": "Spanish",
	"RU": "Russian", "DE": "German", "AT": "German", "IT": "Italian",
	"BR": "Portuguese", "PT": "Portuguese", "CN": "Chinese", "TW": "Chinese", "HK": "Chinese", "KR": "Korean",
}

// DisplayLanguagesFromFlags decodes flag emoji in a release name into canonical display language
// names (e.g. "English", "Japanese"), one per flag, deduplicated and order-preserving. Aggregators
// (AIOStreams) often express a release's languages ONLY as flag emoji, which name parsers and
// CleanReleaseName (which strips emoji) drop — so without this the UI shows no language at all.
// Audio-scoped like LanguagesFromFlags: the UI infers the "Original + Dub" / "Dubbed" badges from
// this list, so subtitle flags must not leak in.
func DisplayLanguagesFromFlags(name string) []string {
	runes := []rune(audioFlagSegment(name))
	seen := make(map[string]bool)
	var out []string
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if a >= 0x1F1E6 && a <= 0x1F1FF && b >= 0x1F1E6 && b <= 0x1F1FF {
			cc := string(rune('A'+(a-0x1F1E6))) + string(rune('A'+(b-0x1F1E6)))
			label, ok := flagDisplayName[cc]
			if !ok {
				label = cc
			}
			if !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
			i++
		}
	}
	return out
}

// dualAudioNameRe matches "dual audio" / "dual-audio" / "dualaudio" (separator optional). In
// fansub convention that specifically means the Japanese original PLUS an English dub, so it is
// credible evidence of English audio on its own. Note it deliberately does NOT match "multi
// audio": in scene naming "MULTi" is the FRENCH convention (VF + original), which names no
// English at all.
var dualAudioNameRe = regexp.MustCompile(`(?i)dual[\s._-]?audio`)

// multiAudioNameRe matches "multi audio" / "multi-audio" / "multiaudio". Unlike "dual audio" it
// names NO language: in scene naming "MULTi" is the FRENCH convention (VF + original), e.g.
// "Nisekoi.S01E04.MULTi.1080p.BluRay.x264-SHiNiGAMi". Deliberately does NOT match bare "multi":
// "Multi Subs" / "[Multiple Subtitle]" are subtitle markers on Japanese-audio releases.
var multiAudioNameRe = regexp.MustCompile(`(?i)multi[\s._-]?audio`)

// englishDubServiceRe matches the Western streaming services whose multi-audio releases always ship
// the English dub alongside the Japanese original — which is what separates a genuine
// "🏷️ VARYG📡 Crunchyroll … 🔍 Multi Subs|Multi Audio" dub from the identically-labelled French
// scene MULTi. Asian-region services are excluded: a Bilibili "multi audio" is Japanese plus
// Chinese, not English.
var englishDubServiceRe = regexp.MustCompile(`(?i)\b(crunchyroll|funimation|netflix|nflx|hidive|hulu|disney|dsnp|amazon|amzn)\b`)

// IsServiceMultiAudio reports whether a release is a multi-audio release from a Western streaming
// service, i.e. one that carries the Japanese original AND an English dub even though the name
// names no language. Shared so the "Original + Dub" badge agrees with the audio tier auto-select
// ranks the release into.
func IsServiceMultiAudio(audioTerms []string, lowerName string) bool {
	hasMulti := multiAudioNameRe.MatchString(lowerName)
	for _, s := range audioTerms {
		if strings.Contains(strings.ToLower(s), "multi") {
			hasMulti = true
			break
		}
	}
	return hasMulti && englishDubServiceRe.MatchString(lowerName)
}

// subtitleOnlyLangTokens are languages a name parser reports that describe SUBTITLES by
// definition — "VOSTFR" is version originale sous-titrée français, i.e. Japanese audio with French
// subs, and reading it as a French dub demotes a perfectly good Japanese release.
var subtitleOnlyLangTokens = map[string]bool{
	"vostfr": true, "vosta": true, "vost": true,
	"softsub": true, "softsubs": true, "hardsub": true, "hardsubs": true,
	"subbed": true, "sub": true, "subs": true,
}

// IsDualAudioRelease reports whether a release declares a dual-audio track set, from a parser's
// audio terms or from the name text.
func IsDualAudioRelease(audioTerms []string, lowerName string) bool {
	for _, s := range audioTerms {
		l := strings.ToLower(s)
		if strings.Contains(l, "dual") || strings.Contains(l, "dub") {
			return true
		}
	}
	return dualAudioNameRe.MatchString(lowerName)
}

// DeriveAudioLanguages returns the languages that describe a release's AUDIO, given the languages
// decoded from flag emoji, the languages a name parser reported, and the subtitle terms it parsed.
//
// This is the ONE place that decides audio-vs-subtitle language, shared by auto-select ranking and
// by the torrent-list badges, because the two disagreeing is a bug in itself: a name parser reports
// SUBTITLE languages in its language field, so a Japanese-audio release reads as an English dub —
// "[Erai-raws] Show - 07 [1080p][Multiple Subtitle] [ENG][POR-BR][SPA-LA]" parses to
// Language=[ENG POR-BR SPA-LA]. Ranking has excluded those since deriveAudioLanguages was written;
// the UI did not, and badged that release "Dubbed".
//
// Resolution order:
//
//  1. Flag emoji win when present: aggregators list audio flags ahead of the 📝 subtitle marker, so
//     DisplayLanguagesFromFlags/LanguagesFromFlags already return audio and nothing else.
//  2. Otherwise the parser's languages — unless the release declares subtitles and declares no
//     audio, in which case those languages belong to the subtitles.
//
// Tokens that are themselves subtitle markers are always dropped. nameMatchOK reports whether the
// caller may additionally look for a language as free text in the name: only when neither better
// source exists, since the text that produced the rejected languages ("[ENG][POR-BR]",
// "Multi Subs|English Subs") is still sitting in the name and would re-credit them.
func DeriveAudioLanguages(flagLangs, parsedLangs, subtitles []string, isDualAudio bool) (langs []string, nameMatchOK bool) {
	if len(flagLangs) > 0 {
		return flagLangs, false
	}
	if len(subtitles) > 0 && !isDualAudio {
		return nil, false
	}
	out := make([]string, 0, len(parsedLangs))
	for _, l := range parsedLangs {
		if !subtitleOnlyLangTokens[strings.ToLower(strings.TrimSpace(l))] {
			out = append(out, l)
		}
	}
	return out, true
}

// MergeLanguages appends extra language labels to base, deduplicating case-insensitively and
// preserving order. Used to fold flag-decoded languages into a parser's language list.
func MergeLanguages(base, extra []string) []string {
	seen := make(map[string]bool, len(base))
	for _, x := range base {
		seen[strings.ToLower(strings.TrimSpace(x))] = true
	}
	out := append([]string{}, base...)
	for _, x := range extra {
		k := strings.ToLower(strings.TrimSpace(x))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	return out
}
