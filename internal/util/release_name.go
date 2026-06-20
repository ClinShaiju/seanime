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
