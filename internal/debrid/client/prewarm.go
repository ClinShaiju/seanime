package debrid_client

import (
	"context"
	"seanime/internal/util"
)

// PrewarmStreams resolves and caches a set of streams ahead of time (the continue-watching
// prewarm: next-up episode of the last few shows). Each target is resolved via the same path as
// the next-episode preload. preloadStream is gated by the PreloadNextStream setting and dedupes
// already-cached / in-flight keys, so calling this on a ticker is cheap and idempotent.
//
// Target SELECTION (which shows, which episode) is the caller's job (core) — this never reorders
// or filters by cache status, preserving the quality-first selection contract.
func (r *Repository) PrewarmStreams(ctx context.Context, targets []*StartStreamOptions) {
	defer util.HandlePanicInModuleThen("debrid/client/PrewarmStreams", func() {})

	if r.settings == nil || !r.settings.PreloadNextStream || r.provider.IsAbsent() {
		return
	}

	for _, opts := range targets {
		if opts == nil {
			continue
		}
		opts.Preload = true
		_ = r.streamManager.preloadStream(ctx, opts)
	}
}

// ClearAllPreloads drops every cached/in-flight preload. Used on provider/account change so a
// stale URL from a previous debrid account is never served.
func (r *Repository) ClearAllPreloads() {
	if r.streamManager == nil {
		return
	}
	r.streamManager.preloadMu.Lock()
	r.streamManager.clearAllPreloadsLocked()
	r.streamManager.preloadMu.Unlock()
}
