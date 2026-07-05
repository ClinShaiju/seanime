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
	"seanime/internal/events"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"time"
)

const (
	// prewarmProbeTimeout bounds the link-validity probe on the background prewarm path (nobody is
	// waiting). prewarmProbeTimeoutPlay is the snappier bound on the play path so a dead link fails
	// fast to the cold-resolve fallback instead of stalling the click; a live link answers in <1s.
	// 4s (was 2s): a congested TorBox CDN routinely takes >2s to first byte, and a false "dead"
	// costs a full cold resolve — worse than 2 extra seconds of probe.
	prewarmProbeTimeout     = 6 * time.Second
	prewarmProbeTimeoutPlay = 4 * time.Second
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

// UserTags is a JSON array of user ids ("[1,3]") recording which users hold a stake in a shared
// prewarm row. Progress cleanup removes only the cleaning user's tag and deletes the row when no
// tags remain, so one user's progress can't wipe rows a slower user still needs.

func parseUserTags(s string) []uint {
	if s == "" {
		return nil
	}
	var tags []uint
	if json.Unmarshal([]byte(s), &tags) != nil {
		return nil
	}
	return tags
}

func encodeUserTags(tags []uint) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(b)
}

func mergeUserTag(tags []uint, id uint) []uint {
	for _, t := range tags {
		if t == id {
			return tags
		}
	}
	return append(tags, id)
}

func removeUserTag(tags []uint, id uint) []uint {
	out := tags[:0]
	for _, t := range tags {
		if t != id {
			out = append(out, t)
		}
	}
	return out
}

func containsUserTag(tags []uint, id uint) bool {
	for _, t := range tags {
		if t == id {
			return true
		}
	}
	return false
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
	// The blob records the entry's ORIGINAL opts, not the caller's: a play-time URL refresh
	// re-persists with the play request's opts, which used to silently rewrite the row's
	// recorded intent (PrewarmMetadata=true rows were downgraded on their first play).
	blobOpts := e.opts
	if blobOpts == nil {
		blobOpts = opts
	}
	blob := persistedActiveStream{
		Opts: blobOpts, StreamUrl: e.streamUrl, FileId: e.fileId, Filepath: e.filepath,
		Media: e.media, Torrent: e.torrent, TorrentItemId: e.torrentItemId,
		ResolvedAt: e.resolvedAt, UrlResolvedAt: e.urlResolvedAt, TtlNanos: int64(e.ttl),
	}
	data, err := json.Marshal(&blob)
	if err != nil {
		return
	}
	// Tag the acting user as a stakeholder (merged with existing tags — an upsert replaces the
	// whole row, and dropping another user's tag would re-expose their rows to cross-user cleanup).
	profileHash := profileHashFor(s.repository.resolveAutoSelectProfile(opts.UserID))
	tags := []uint{opts.UserID}
	if existing, ok := s.repository.db.GetDebridPrewarm(acct, opts.MediaId, opts.EpisodeNumber, opts.AniDBEpisode, profileHash); ok {
		tags = mergeUserTag(parseUserTags(existing.UserTags), opts.UserID)
	}
	_ = s.repository.db.UpsertDebridPrewarm(&models.DebridPrewarm{
		AccountHash:   acct,
		MediaId:       opts.MediaId,
		EpisodeNumber: opts.EpisodeNumber,
		AniDBEpisode:  opts.AniDBEpisode,
		ProfileHash:   profileHash,
		UserTags:      encodeUserTags(tags),
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
		// priority=true always: a hydrated row is continue-watching-class (only auto-select
		// prewarms are persisted) and bounded by the row TTLs. With opts.Priority (false on the
		// play path) + the row's old resolvedAt, the CURRENTLY PLAYING entry was the speculative
		// budget's first eviction candidate.
		resolvedAt: p.ResolvedAt, urlResolvedAt: urlResolvedAt, ttl: ttl, priority: true,
	}
	key := preloadKey(opts)
	s.preloadMu.Lock()
	s.evictIfNeededLocked(true) // priority-class store: TTL sweep only, no speculative eviction
	s.preloads[key] = entry
	s.preloadMu.Unlock()

	// If we re-resolved, write the fresh URL back so the next consumer on this account reuses it.
	if streamUrl != p.StreamUrl {
		s.persistPrewarm(opts, entry)
	}
	s.invalidatePrewarmBadges(opts) // a fresh in-memory entry exists now — refresh badges
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
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true
	}
	// A throttled (429) or transiently-erroring link is ALIVE, not dead — treating it as dead
	// triggered a needless requestdl re-resolve exactly when the CDN was telling us to back off,
	// amplifying the rate-limit. Only permanent errors (403/404/410…) mean the link is gone.
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
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

// CleanupWatchedPrewarms drops prewarm state for episodes the user has already watched:
// in-memory entries and shared DB rows with episode_number < keepFromEp. The caller (core tick)
// passes progress-1, i.e. the "n-2 rule": the last-watched episode AND its predecessor stay
// (AniList progress syncs at ~80% of an episode, so a plain <progress cutoff wiped the previous
// episode's cache minutes into the next one). Shared DB rows are per-user refcounted via
// UserTags: this user's tag is removed and the row is deleted only when no stakeholders remain,
// so one user's progress can't wipe rows a slower user on the same account still needs.
func (r *Repository) CleanupWatchedPrewarms(userID uint, mediaId, keepFromEp int) {
	defer util.HandlePanicInModuleThen("debrid/client/CleanupWatchedPrewarms", func() {})
	changed := false
	// In-memory entries — only if the user's StreamManager exists (don't create one to clean it).
	if sm, ok := r.streamManagers.Get(userID); ok {
		sm.preloadMu.Lock()
		for k, e := range sm.preloads {
			if e == nil || e.opts == nil || e.opts.MediaId != mediaId || e.opts.EpisodeNumber >= keepFromEp {
				continue
			}
			if k == sm.lastConsumedKey {
				continue // currently/last playing — its consume/transition path owns its lifecycle
			}
			// Release any prewarmed MKV metadata (font attachments in RAM) with the entry.
			if dm := sm.ds(e.opts); dm != nil {
				dm.DropStreamMetadata(e.streamUrl)
			}
			delete(sm.preloads, k)
			changed = true
		}
		sm.preloadMu.Unlock()
	}
	if r.db != nil {
		if acct := r.accountHash(); acct != "" {
			if rows, err := r.db.ListDebridPrewarms(); err == nil {
				for _, row := range rows {
					if row.AccountHash != acct || row.MediaId != mediaId || row.EpisodeNumber >= keepFromEp {
						continue
					}
					tags := removeUserTag(parseUserTags(row.UserTags), userID)
					if len(tags) == 0 {
						// Untagged (legacy) or this was the last stakeholder → drop the row.
						_ = r.db.DeleteDebridPrewarmByID(row.ID)
					} else {
						// Another user is still behind this episode — unref only.
						_ = r.db.UpdateDebridPrewarmUserTags(row.ID, encodeUserTags(tags))
					}
					changed = true // either way this user's badge for the row goes away
				}
			}
		}
	}
	if changed {
		if em := r.evFor(userID); em != nil {
			em.SendEvent(events.InvalidateQueries, []string{events.DebridGetPrewarmStatusEndpoint})
		}
	}
}

// PrewarmStatusItem reports that a given episode is prewarmed (will play instantly). Metadata=true
// means its MKV metadata is parsed and cached RIGHT NOW (instant first frame) — a live check
// against the parser cache, not the recorded prewarm intent, so the badge can't claim warmth the
// play won't get. Consumed by the UI to badge episodes; read-only, never triggers a resolve.
type PrewarmStatusItem struct {
	MediaId       int    `json:"mediaId"`
	EpisodeNumber int    `json:"episodeNumber"`
	AniDBEpisode  string `json:"anidbEpisode"`
	Metadata      bool   `json:"metadata"`
}

// GetPrewarmStatus returns the set of episodes that are prewarmed for the user: their own in-memory
// preloads plus the fresh shared-DB rows for their account+profile that they hold a stake in.
// Pure read — no resolve, no stream-manager creation.
func (r *Repository) GetPrewarmStatus(userID uint) []PrewarmStatusItem {
	defer util.HandlePanicInModuleThen("debrid/client/GetPrewarmStatus", func() {})
	out := []PrewarmStatusItem{}
	if r.settings == nil || !r.settings.PreloadNextStream || r.provider.IsAbsent() {
		return out
	}

	// Metadata warmth = the user's directstream parser cache holds the entry's CURRENT URL.
	dm := r.dsFor(userID)
	metaWarm := func(streamUrl string) bool {
		return dm != nil && dm.HasStreamMetadata(streamUrl)
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
			add(e.opts.MediaId, e.opts.EpisodeNumber, e.opts.AniDBEpisode, metaWarm(e.streamUrl))
		}
		sm.preloadMu.Unlock()
	}

	// Shared DB: fresh rows for this account + profile that this user holds a stake in (tagged,
	// or legacy untagged). Rows held only by OTHER users are skipped — they'd still hydrate
	// instantly at play, but badging them re-flames episodes this user already watched.
	if r.db != nil {
		if acct := r.accountHash(); acct != "" {
			profile := profileHashFor(r.resolveAutoSelectProfile(userID))
			if rows, err := r.db.ListDebridPrewarms(); err == nil {
				for _, row := range rows {
					if row.AccountHash != acct || row.ProfileHash != profile {
						continue
					}
					if tags := parseUserTags(row.UserTags); len(tags) > 0 && !containsUserTag(tags, userID) {
						continue
					}
					ttl := time.Duration(row.TtlNanos)
					if ttl <= 0 || time.Since(row.ResolvedAt) > ttl {
						continue
					}
					var p persistedActiveStream
					if json.Unmarshal([]byte(row.Data), &p) != nil {
						continue
					}
					add(row.MediaId, row.EpisodeNumber, row.AniDBEpisode, metaWarm(p.StreamUrl))
				}
			}
		}
	}
	return out
}
