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

// LanguagesFromFlags decodes flag emoji (regional-indicator pairs) in a release name into
// language tokens, so language scoring can see languages that are only expressed as flags.
// Unknown countries fall back to their lowercase code so they still count as a declared
// (non-preferred) language. Returns deduplicated lowercase tokens.
func LanguagesFromFlags(name string) []string {
	runes := []rune(name)
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
func DisplayLanguagesFromFlags(name string) []string {
	runes := []rune(name)
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
