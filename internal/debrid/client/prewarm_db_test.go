package debrid_client

import (
	"seanime/internal/library/anime"
	"testing"
)

// TestProfileHashFor guards the quality-over-speed reuse gate: a shared prewarm row may only be
// reused by a matching auto-select profile. So identical profiles MUST hash equal (or reuse never
// hits) and different profiles MUST hash differently (or user B could play A's wrong-quality pick).
func TestProfileHashFor(t *testing.T) {
	if got := profileHashFor(nil); got != "default" {
		t.Fatalf("nil profile should hash to 'default', got %q", got)
	}

	p1 := &anime.AutoSelectProfile{Resolutions: []string{"1080p"}, MinSeeders: 0}
	p1b := &anime.AutoSelectProfile{Resolutions: []string{"1080p"}, MinSeeders: 0}
	p2 := &anime.AutoSelectProfile{Resolutions: []string{"720p"}, MinSeeders: 0}

	if profileHashFor(p1) != profileHashFor(p1b) {
		t.Error("identical profiles must hash equal, else cross-user reuse never hits")
	}
	if profileHashFor(p1) == profileHashFor(p2) {
		t.Error("different profiles must hash differently, else a user could play a wrong-quality selection")
	}
}
