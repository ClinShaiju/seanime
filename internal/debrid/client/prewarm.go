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

	// Serialize the scheduled fan-out: drain in ONE background goroutine, spacing each kickoff via
	// prewarmLimiter, so the continue-watching tick no longer hits TorBox simultaneously (the
	// concurrent N_users×N burst was a prime 429 source). Returns immediately so the tick isn't
	// blocked; client-triggered preloads (play @3s, hover) stay direct and unthrottled.
	go func() {
		defer util.HandlePanicInModuleThen("debrid/client/PrewarmStreams/drain", func() {})
		for _, opts := range targets {
			if opts == nil {
				continue
			}
			if r.prewarmLimiter != nil {
				if err := r.prewarmLimiter.Wait(ctx); err != nil {
					return // context cancelled
				}
			}
			opts.Preload = true
			// Per-user: prewarm into the target user's own StreamManager so each user's preload
			// cache is theirs and is consumed when THEY play.
			_ = r.smFor(opts.UserID).preloadStream(ctx, opts)
		}
	}()
}

// ClearAllPreloads drops every cached/in-flight preload across ALL users. Used on
// provider/account change so a stale URL from a previous debrid account is never served.
func (r *Repository) ClearAllPreloads() {
	if r.streamManagers == nil {
		return
	}
	r.streamManagers.Range(func(_ uint, sm *StreamManager) bool {
		sm.preloadMu.Lock()
		sm.clearAllPreloadsLocked()
		sm.preloadMu.Unlock()
		return true
	})
}
