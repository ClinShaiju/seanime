package directstream

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// cdnPerTokenConns bounds simultaneous CDN connections to a SINGLE debrid download link (token).
// TorBox throttles per-link, so a burst of connections to one token at stream-start — the serve
// path's head (bytes=0-) + the player's tail-index probe + a direct metadata read + a prewarm warm
// read — trips a 429 even when the account is otherwise idle. The cap is PER-TOKEN, not per-host, so
// it never throttles legitimate concurrency across different streams or users. 2 lets the player's
// head + tail probe coexist; anything beyond queues briefly instead of 429ing.
//
// ponytail: fixed cap, env-tunable. If TorBox's real per-link limit turns out to be 1 or 3, set
// SEANIME_DIRECTSTREAM_PER_TOKEN_CONNS rather than editing code.
var cdnPerTokenConns = func() int {
	if v := os.Getenv("SEANIME_DIRECTSTREAM_PER_TOKEN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}()

var cdnTokenGateInst = &cdnTokenGate{sems: map[string]chan struct{}{}}

type cdnTokenGate struct {
	mu   sync.Mutex
	sems map[string]chan struct{}
}

// cdnTokenKey strips the one-time auth query (?token=…) so re-resolves of the same file share one
// gate — TorBox throttles the link/file, the query is just auth.
func cdnTokenKey(url string) string {
	if i := strings.IndexByte(url, '?'); i != -1 {
		return url[:i]
	}
	return url
}

func (g *cdnTokenGate) semFor(token string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	sem, ok := g.sems[token]
	if !ok {
		// ponytail: map grows by unique-file, never pruned; bounded by files streamed since boot and
		// cleared on restart. Add LRU eviction only if a long-lived server's footprint proves it.
		sem = make(chan struct{}, cdnPerTokenConns)
		g.sems[token] = sem
	}
	return sem
}

// acquire blocks until a slot is free for the token or ctx is cancelled. The returned release is
// safe to call any number of times (only the first frees the slot). An empty token is ungated.
func (g *cdnTokenGate) acquire(ctx context.Context, token string) (release func(), err error) {
	if token == "" {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sem := g.semFor(token)
	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

// tryAcquire takes a slot only if one is free RIGHT NOW; ok=false means the token is busy.
// For speculative work (CDN warms): a contended token is being actively streamed, which makes
// warming it pointless — and a PARKED warm goroutine would race the user's next range request
// for the freed slot (select order is random), putting a 16-48MB warm read ahead of a seek.
func (g *cdnTokenGate) tryAcquire(token string) (release func(), ok bool) {
	if token == "" {
		return func() {}, true
	}
	sem := g.semFor(token)
	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, true
	default:
		return func() {}, false
	}
}

// gatedReadSeekCloser holds a per-token gate slot for the lifetime of a reader, releasing it on Close.
type gatedReadSeekCloser struct {
	io.ReadSeekCloser
	release   func()
	closeOnce sync.Once
}

func (g *gatedReadSeekCloser) Close() error {
	err := g.ReadSeekCloser.Close()
	g.closeOnce.Do(g.release)
	return err
}
