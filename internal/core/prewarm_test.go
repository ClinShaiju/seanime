package core

import (
	"testing"
	"time"
)

func TestSelectPrewarmTargets(t *testing.T) {
	base := time.Now()

	cands := []prewarmCandidate{
		{mediaId: 1, progress: 3, epCount: 12, updated: base.Add(-3 * time.Hour)}, // older
		{mediaId: 2, progress: 5, epCount: 24, updated: base.Add(-1 * time.Hour)}, // newer
		{mediaId: 3, progress: 0, epCount: 12, updated: base.Add(-30 * time.Minute)}, // never started -> skip
		{mediaId: 4, progress: 12, epCount: 12, updated: base.Add(-10 * time.Minute)}, // caught up -> skip
		{mediaId: 5, progress: 8, epCount: -1, updated: base.Add(-5 * time.Minute)}, // unknown count -> allowed
		{mediaId: 6, progress: 2, epCount: 12, updated: base.Add(-2 * time.Hour)},
	}

	got := selectPrewarmTargets(cands, 3, 3)

	// All non-airing -> metadata bucket (cap 3). Most-recent-first (5:-5m, 4:-10m, 3:-30m, 2:-1h,
	// 6:-2h, 1:-3h), skipping not-started (3) and caught-up (4): expect 5, 2, 6, all metadata.
	if len(got) != 3 {
		t.Fatalf("expected 3 targets, got %d: %+v", len(got), got)
	}
	want := []prewarmTarget{
		{mediaId: 5, nextEp: 9, metadata: true},
		{mediaId: 2, nextEp: 6, metadata: true},
		{mediaId: 6, nextEp: 3, metadata: true},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("target[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestSelectPrewarmTargetsAiring(t *testing.T) {
	base := time.Now()
	cands := []prewarmCandidate{
		{mediaId: 1, progress: 5, epCount: 12, updated: base.Add(-1 * time.Minute)},
		{mediaId: 2, progress: 5, epCount: 12, updated: base.Add(-2 * time.Minute)},
		{mediaId: 3, progress: 5, epCount: 12, updated: base.Add(-3 * time.Minute)},
		// Releasing, latest just dropped (progress+1 == epCount).
		{mediaId: 4, progress: 7, epCount: 8, updated: base.Add(-10 * 24 * time.Hour), releasing: true},
		// Releasing but behind by more than one: still keep its next-to-watch link alive (URL-only).
		{mediaId: 5, progress: 2, epCount: 8, updated: base.Add(-11 * 24 * time.Hour), releasing: true},
	}

	got := selectPrewarmTargets(cands, 3, 3)

	// Bucket 1 (non-airing, metadata): 1,2,3 -> next 6. Bucket 2 (airing, URL-only): 4 -> 8, 5 -> 3.
	want := []prewarmTarget{
		{mediaId: 1, nextEp: 6, metadata: true},
		{mediaId: 2, nextEp: 6, metadata: true},
		{mediaId: 3, nextEp: 6, metadata: true},
		{mediaId: 4, nextEp: 8, metadata: false},
		{mediaId: 5, nextEp: 3, metadata: false},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d targets, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("target[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestSelectPrewarmTargetsEmpty(t *testing.T) {
	if got := selectPrewarmTargets(nil, 3, 3); len(got) != 0 {
		t.Fatalf("expected no targets, got %+v", got)
	}
	// All caught up.
	cands := []prewarmCandidate{{mediaId: 1, progress: 12, epCount: 12, updated: time.Now()}}
	if got := selectPrewarmTargets(cands, 3, 3); len(got) != 0 {
		t.Fatalf("expected no targets for caught-up show, got %+v", got)
	}
}
