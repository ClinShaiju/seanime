package debrid_client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"seanime/internal/database/models"
	"seanime/internal/debrid/debrid"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"time"
)

const (
	// prewarmProbeTimeout bounds the link-validity probe on the background prewarm path (nobody is
	// waiting). prewarmProbeTimeoutPlay is the snappier bound on the play path so a dead link fails
	// fast to the cold-resolve fallback instead of stalling the click; a live link answers in <1s.
	prewarmProbeTimeout     = 6 * time.Second
	prewarmProbeTimeoutPlay = 2 * time.Second
)

// accountHash partitions shared prewarm rows by TorBox account. The raw API key is never stored —
// only this hash — and reuse is valid only within the same account (the torrent item, cache, and
// rate limits all live behind the key). Today there's one shared server key; per-user keys (P4)
// resolve to distinct hashes automatically.
func (r *Repository) accountHash() string {
	if r.settings == nil || r.settings.ApiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(r.settings.Provider + "|" + r.settings.ApiKey))
	return hex.EncodeToString(sum[:8])
}

// profileHashFor fingerprints an auto-select profile so DB reuse is gated to matching profiles: a
// user on a different profile simply misses the shared row and resolves their own. This is what
// keeps the quality-over-speed rule mechanical (no serving B a selection computed for A's profile).
func profileHashFor(p *anime.AutoSelectProfile) string {
	if p == nil {
		return "default"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "default"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// persistPrewarm writes (or refreshes) the shared, account-partitioned DB row for a resolved
// prewarm/stream, so any user on the same account reuses it and it survives a restart. Best-effort;
// reuses the persistedActiveStream blob format. Never reorders selection — it only records what was
// already chosen.
func (s *StreamManager) persistPrewarm(opts *StartStreamOptions, e *preloadedDebridStream) {
	defer util.HandlePanicInModuleThen("debrid/client/persistPrewarm", func() {})
	if s.repository.db == nil || opts == nil || e == nil || e.streamUrl == "" {
		return
	}
	if !opts.AutoSelect {
		return // only the quality-ranked auto-select result is shareable; a manual pick is user-specific
	}
	acct := s.repository.accountHash()
	if acct == "" {
		return
	}
	blob := persistedActiveStream{
		Opts: opts, StreamUrl: e.streamUrl, FileId: e.fileId, Filepath: e.filepath,
		Media: e.media, Torrent: e.torrent, TorrentItemId: e.torrentItemId,
		ResolvedAt: e.resolvedAt, UrlResolvedAt: e.urlResolvedAt, TtlNanos: int64(e.ttl),
	}
	data, err := json.Marshal(&blob)
	if err != nil {
		return
	}
	_ = s.repository.db.UpsertDebridPrewarm(&models.DebridPrewarm{
		AccountHash:   acct,
		MediaId:       opts.MediaId,
		EpisodeNumber: opts.EpisodeNumber,
		AniDBEpisode:  opts.AniDBEpisode,
		ProfileHash:   profileHashFor(s.repository.resolveAutoSelectProfile(opts.UserID)),
		Data:          string(data),
		ResolvedAt:    e.resolvedAt,
		UrlResolvedAt: e.urlResolvedAt,
		TtlNanos:      int64(e.ttl),
	})
}

// hydratePrewarmFromDB tries to satisfy a prewarm from the shared DB instead of re-resolving
// (cross-user reuse on the same account + restart survival). On a fresh row it VALIDATES the cached
// link with a cheap CDN probe; if the link is dead it re-resolves from the already-added
// torrentItemId (no createtorrent); if the item itself is gone it drops the row and reports a miss
// so the caller falls back to a full resolve. On success it populates the in-memory preload entry
// and returns true. Selection is never re-ranked — it replays the recorded choice.
func (s *StreamManager) hydratePrewarmFromDB(ctx context.Context, opts *StartStreamOptions, probeTimeout time.Duration) (*preloadedDebridStream, bool) {
	defer util.HandlePanicInModuleThen("debrid/client/hydratePrewarmFromDB", func() {})
	if s.repository.db == nil || opts == nil {
		return nil, false
	}
	if !opts.AutoSelect {
		return nil, false // only auto-select rows are shared; a manual pick never reuses one
	}
	acct := s.repository.accountHash()
	if acct == "" {
		return nil, false
	}
	profileHash := profileHashFor(s.repository.resolveAutoSelectProfile(opts.UserID))
	rec, ok := s.repository.db.GetDebridPrewarm(acct, opts.MediaId, opts.EpisodeNumber, opts.AniDBEpisode, profileHash)
	if !ok {
		return nil, false
	}
	ttl := time.Duration(rec.TtlNanos)
	if ttl <= 0 || time.Since(rec.ResolvedAt) > ttl {
		_ = s.repository.db.DeleteDebridPrewarmByID(rec.ID) // selection expired
		return nil, false
	}
	var p persistedActiveStream
	if err := json.Unmarshal([]byte(rec.Data), &p); err != nil || p.StreamUrl == "" {
		return nil, false
	}

	streamUrl := p.StreamUrl
	urlResolvedAt := p.UrlResolvedAt
	// Validate the link cheaply (CDN range probe — NOT a rate-limited API call). Re-resolve from the
	// torrentItemId only if it's dead; drop the row if the torrent item itself is gone.
	if !probeStreamURL(ctx, streamUrl, probeTimeout) {
		fresh, rerr := s.reresolveURL(ctx, p.TorrentItemId, p.FileId)
		if rerr != nil || fresh == "" {
			_ = s.repository.db.DeleteDebridPrewarmByID(rec.ID)
			return nil, false
		}
		streamUrl = fresh
		urlResolvedAt = time.Now()
	}

	entry := &preloadedDebridStream{
		opts: opts, streamUrl: streamUrl, fileId: p.FileId, filepath: p.Filepath,
		media: p.Media, torrent: p.Torrent, torrentItemId: p.TorrentItemId,
		resolvedAt: p.ResolvedAt, urlResolvedAt: urlResolvedAt, ttl: ttl, priority: opts.Priority,
	}
	key := preloadKey(opts)
	s.preloadMu.Lock()
	s.preloads[key] = entry
	s.preloadMu.Unlock()

	// If we re-resolved, write the fresh URL back so the next consumer on this account reuses it.
	if streamUrl != p.StreamUrl {
		s.persistPrewarm(opts, entry)
	}
	s.repository.logger.Debug().Int("mediaId", opts.MediaId).Int("episode", opts.EpisodeNumber).Msg("debridstream: Hydrated prewarm from shared DB (no re-resolve)")
	return entry, true
}

// reresolveURL gets a fresh CDN link from an already-added torrent item (no createtorrent).
func (s *StreamManager) reresolveURL(ctx context.Context, torrentItemId, fileId string) (string, error) {
	if torrentItemId == "" {
		return "", fmt.Errorf("no torrent item id")
	}
	provider, err := s.repository.GetProvider()
	if err != nil {
		return "", err
	}
	itemCh := make(chan debrid.TorrentItem, 1)
	go func() {
		for range itemCh { //nolint:revive
		}
	}()
	url, err := provider.GetTorrentStreamUrl(ctx, debrid.StreamTorrentOptions{ID: torrentItemId, FileId: fileId}, itemCh)
	close(itemCh)
	return url, err
}

// probeStreamURL reports whether a debrid CDN URL still serves bytes (a 1-byte range GET). This hits
// the CDN, not the rate-limited TorBox API, so it's cheap to use as a liveness check before reusing
// a cached link (avoids the blind time-based re-resolve while the link is still valid).
func probeStreamURL(ctx context.Context, url string, timeout time.Duration) bool {
	if url == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// SweepExpiredPrewarms drops shared prewarm rows past their TTL. Cheap (small table); runs on the
// continue-watching tick. Torrents themselves are left on the account — cached torrents are free to
// keep (they don't count against the active-torrent cap), and TorBox holds them anyway.
func (r *Repository) SweepExpiredPrewarms() {
	defer util.HandlePanicInModuleThen("debrid/client/SweepExpiredPrewarms", func() {})
	if r.db == nil {
		return
	}
	rows, err := r.db.ListDebridPrewarms()
	if err != nil {
		return
	}
	for _, row := range rows {
		ttl := time.Duration(row.TtlNanos)
		if ttl <= 0 || time.Since(row.ResolvedAt) > ttl {
			_ = r.db.DeleteDebridPrewarmByID(row.ID)
		}
	}
}

// PrewarmStatusItem reports that a given episode is prewarmed (will play instantly). Metadata=true
// means it's also metadata/CDN-warmed (the tier-1 target — instant first frame too). Consumed by the
// UI to badge episodes; read-only, never triggers a resolve.
type PrewarmStatusItem struct {
	MediaId       int    `json:"mediaId"`
	EpisodeNumber int    `json:"episodeNumber"`
	AniDBEpisode  string `json:"anidbEpisode"`
	Metadata      bool   `json:"metadata"`
}

// GetPrewarmStatus returns the set of episodes that are prewarmed for the user: their own in-memory
// preloads plus the fresh shared-DB rows for their account+profile (which they'd reuse instantly).
// Pure read — no resolve, no manager creation.
func (r *Repository) GetPrewarmStatus(userID uint) []PrewarmStatusItem {
	defer util.HandlePanicInModuleThen("debrid/client/GetPrewarmStatus", func() {})
	out := []PrewarmStatusItem{}
	if r.settings == nil || !r.settings.PreloadNextStream || r.provider.IsAbsent() {
		return out
	}

	type key struct{ media, ep int }
	seen := make(map[key]int) // key -> index into out (dedup; Metadata=true wins)
	add := func(media, ep int, anidb string, meta bool) {
		k := key{media, ep}
		if idx, ok := seen[k]; ok {
			if meta {
				out[idx].Metadata = true
			}
			return
		}
		seen[k] = len(out)
		out = append(out, PrewarmStatusItem{MediaId: media, EpisodeNumber: ep, AniDBEpisode: anidb, Metadata: meta})
	}

	// In-memory: only if the user's StreamManager already exists (don't create one for a read).
	if sm, ok := r.streamManagers.Get(userID); ok {
		sm.preloadMu.Lock()
		for _, e := range sm.preloads {
			if e == nil || e.opts == nil || time.Since(e.resolvedAt) > e.ttl {
				continue
			}
			add(e.opts.MediaId, e.opts.EpisodeNumber, e.opts.AniDBEpisode, e.opts.PrewarmMetadata)
		}
		sm.preloadMu.Unlock()
	}

	// Shared DB: fresh rows for this account + profile.
	if r.db != nil {
		if acct := r.accountHash(); acct != "" {
			profile := profileHashFor(r.resolveAutoSelectProfile(userID))
			if rows, err := r.db.ListDebridPrewarms(); err == nil {
				for _, row := range rows {
					if row.AccountHash != acct || row.ProfileHash != profile {
						continue
					}
					ttl := time.Duration(row.TtlNanos)
					if ttl <= 0 || time.Since(row.ResolvedAt) > ttl {
						continue
					}
					meta := false
					var p persistedActiveStream
					if json.Unmarshal([]byte(row.Data), &p) == nil && p.Opts != nil {
						meta = p.Opts.PrewarmMetadata
					}
					add(row.MediaId, row.EpisodeNumber, row.AniDBEpisode, meta)
				}
			}
		}
	}
	return out
}
