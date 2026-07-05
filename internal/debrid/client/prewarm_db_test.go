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

// TestUserTags guards the shared-row refcounting round-trip: tags survive encode/parse, merge is
// idempotent, and removal of the last stakeholder yields an empty set (row-delete signal).
func TestUserTags(t *testing.T) {
	tags := mergeUserTag(nil, 1)
	tags = mergeUserTag(tags, 3)
	tags = mergeUserTag(tags, 1) // idempotent
	if len(tags) != 2 || !containsUserTag(tags, 1) || !containsUserTag(tags, 3) {
		t.Fatalf("merge produced %v, want [1 3]", tags)
	}
	rt := parseUserTags(encodeUserTags(tags))
	if len(rt) != 2 || !containsUserTag(rt, 1) || !containsUserTag(rt, 3) {
		t.Fatalf("encode/parse round-trip produced %v", rt)
	}
	rt = removeUserTag(rt, 1)
	if len(rt) != 1 || containsUserTag(rt, 1) {
		t.Fatalf("remove(1) produced %v", rt)
	}
	if rest := removeUserTag(rt, 3); len(rest) != 0 {
		t.Fatalf("removing last stakeholder should empty the set, got %v", rest)
	}
	if parseUserTags("") != nil || parseUserTags("garbage") != nil {
		t.Fatal("empty/garbage tags must parse to nil (legacy rows)")
	}
	if encodeUserTags(nil) != "" {
		t.Fatal("empty tag set must encode to empty string")
	}
}

// TestCleanupWatchedPrewarms guards the progress-aware cleanup rule: entries below keepFromEp are
// dropped (they flame-badged already-watched episodes for 24h), entries at/above it are kept
// (the core tick passes progress-1, the "n-2 rule"), other shows are untouched, and the in-play
// entry is never touched regardless of episode number.
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
