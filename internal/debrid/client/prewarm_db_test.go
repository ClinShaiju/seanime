package debrid_client

import (
	"seanime/internal/library/anime"
	"seanime/internal/util/result"
	"testing"
	"time"
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

// TestCleanupWatchedPrewarms guards the progress-aware cleanup rule: entries below progress are
// dropped (they flame-badged already-watched episodes for 24h), the last-watched episode is kept
// for instant replay, next-up and other shows are untouched, and the in-play entry is never
// touched regardless of episode number.
func TestCleanupWatchedPrewarms(t *testing.T) {
	repo := &Repository{streamManagers: result.NewMap[uint, *StreamManager]()}
	sm := NewStreamManager(repo)
	repo.streamManagers.Set(1, sm)

	add := func(mediaId, ep int) string {
		opts := &StartStreamOptions{MediaId: mediaId, EpisodeNumber: ep, AutoSelect: true}
		k := preloadKey(opts)
		sm.preloads[k] = &preloadedDebridStream{opts: opts, resolvedAt: time.Now(), ttl: time.Hour}
		return k
	}
	watched := add(100, 3) // behind progress → dropped
	replay := add(100, 5)  // == progress (last watched) → kept for instant replay
	nextUp := add(100, 6)  // next-up → kept
	other := add(200, 1)   // different show → untouched
	inPlay := add(100, 2)  // behind progress but currently playing → never touched
	sm.lastConsumedKey = inPlay

	repo.CleanupWatchedPrewarms(1, 100, 5)

	if _, ok := sm.preloads[watched]; ok {
		t.Error("episode below progress must be dropped")
	}
	for name, k := range map[string]string{"last-watched": replay, "next-up": nextUp, "other show": other, "in-play": inPlay} {
		if _, ok := sm.preloads[k]; !ok {
			t.Errorf("%s entry must be kept", name)
		}
	}
}
