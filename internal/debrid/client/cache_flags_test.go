package debrid_client

import "testing"

func TestParseDebridCacheFlag(t *testing.T) {
	tests := []struct {
		name        string
		torrentName string
		providerID  string
		wantCached  bool
		wantKnown   bool
	}{
		// AIOStreams / MediaFusion emoji flags
		{"torbox bolt cached", "⚡ TB | Wistoria S2 - 01 [1080p]", "torbox", true, true},
		{"hourglass uncached", "⏳ TB | Wistoria S2 - 01 [1080p]", "torbox", false, true},
		{"down arrow uncached", "⬇ Wistoria S2 - 01", "torbox", false, true},
		{"both markers -> defer to API", "⚡ TB ⏳ RD | Show - 01", "torbox", false, false},
		// Torrentio-style tags
		{"torrentio cached tag", "[TB+] Show - 01 [1080p]", "torbox", true, true},
		{"torrentio cached bare", "TB+ Show - 01", "torbox", true, true},
		{"torrentio download uncached", "[TB download] Show - 01", "torbox", false, true},
		{"realdebrid cached tag", "[RD+] Show - 01", "realdebrid", true, true},
		{"alldebrid download", "[AD download] Show - 01", "alldebrid", false, true},
		// Wrong provider / no flag -> unknown, caller falls back to API
		{"flag for other provider", "[RD+] Show - 01", "torbox", false, false},
		{"plain name no flag", "[SubsPlease] Show - 01 (1080p).mkv", "torbox", false, false},
		{"empty name", "", "torbox", false, false},
		{"unknown provider", "⚡ Show - 01", "premiumize", true, true}, // lone bolt still resolves
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cached, known := parseDebridCacheFlag(tt.torrentName, tt.providerID)
			if cached != tt.wantCached || known != tt.wantKnown {
				t.Fatalf("parseDebridCacheFlag(%q, %q) = (%v, %v), want (%v, %v)",
					tt.torrentName, tt.providerID, cached, known, tt.wantCached, tt.wantKnown)
			}
		})
	}
}
