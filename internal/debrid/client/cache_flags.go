package debrid_client

import "strings"

// Cache markers that torrent sources (AIOStreams, MediaFusion, ...) embed in result names.
const (
	cacheBoltMarker      = "⚡" // ⚡ cached / instant
	cacheHourglassMarker = "⏳" // ⏳ uncached / must be downloaded first
	cacheDownArrowMarker = "⬇" // ⬇ uncached
)

// debridServiceCode maps a debrid provider ID to the short service code that torrent
// sources tag results with (e.g. "[TB+]", "TB ⚡"). Empty when unknown.
func debridServiceCode(providerID string) string {
	switch providerID {
	case "torbox":
		return "tb"
	case "realdebrid":
		return "rd"
	case "alldebrid":
		return "ad"
	default:
		return ""
	}
}

// parseDebridCacheFlag reads a cache indicator for the given debrid provider from a
// torrent name as annotated by the source.
//
// Returns (cached, known). known is false when the name carries no recognizable flag
// for this provider, so the caller can fall back to the provider's instant-availability
// API. This is free (string parse, no network) and is the only cache signal available
// for RealDebrid/AllDebrid, whose instant-availability endpoints no longer work.
//
// ponytail: heuristic over two common conventions; falls back to the API when unsure,
// so a miss is safe. Add more source formats here if a provider needs them.
func parseDebridCacheFlag(name, providerID string) (cached bool, known bool) {
	if name == "" {
		return false, false
	}
	lower := strings.ToLower(name)
	code := debridServiceCode(providerID)

	// 1. Torrentio-style tags tied to the service: "[tb+]"/"tb+" cached, "tb download" uncached.
	if code != "" {
		if strings.Contains(lower, "["+code+"+]") || strings.Contains(lower, code+"+") {
			return true, true
		}
		if strings.Contains(lower, code+" download") {
			return false, true
		}
	}

	// 2. Emoji flags (AIOStreams / MediaFusion). A lone marker refers to the configured
	//    provider; when both appear (multi-service listing) we can't attribute it, so we
	//    defer to the API.
	hasBolt := strings.Contains(name, cacheBoltMarker)
	hasUncached := strings.Contains(name, cacheHourglassMarker) || strings.Contains(name, cacheDownArrowMarker)
	if hasBolt && !hasUncached {
		return true, true
	}
	if hasUncached && !hasBolt {
		return false, true
	}

	return false, false
}
