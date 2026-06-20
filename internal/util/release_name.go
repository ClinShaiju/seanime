package util

import "regexp"

// sizeTokenRe matches file-size tokens like "833 MB", "950MB", "4.94 GB", "227.2 MiB".
// Units are restricted to KB/MB/GB/TB (binary or decimal) so it never touches resolutions
// ("1080p"), bit-depth ("10bit"), codecs ("x265"), or years.
var sizeTokenRe = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s*[KMGT]i?B\b`)

// StripSizeTokens removes file-size tokens from a release name so parsers don't mistake the
// size value for an episode number (e.g. "Witch Hat Atelier (2026) 833 MB" → episode 833).
func StripSizeTokens(name string) string {
	return sizeTokenRe.ReplaceAllString(name, " ")
}
