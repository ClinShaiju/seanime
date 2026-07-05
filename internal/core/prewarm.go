package core

import (
	"context"
	"seanime/internal/api/anilist"
	"seanime/internal/continuity"
	debrid_client "seanime/internal/debrid/client"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"sort"
	"strconv"
	"time"
)

const (
	prewarmContinueWatchingCount = 3                // next-up episode of the last N watched shows
	prewarmJustAiredCount        = 3                // + new episode of up to N currently-airing shows
	prewarmInitialDelay          = 30 * time.Second // let the collection + debrid settings load first
	prewarmInterval              = 10 * time.Minute // re-check targets well within the selection/URL TTLs
	// prewarmRecencyCutoff drops candidates the user hasn't touched in a while — sessions live for
	// the server process lifetime, so without this an inactive user's releasing-show targets are
	// re-resolved forever. A returning user is re-picked the moment they watch anything.
	// ponytail: 14d recency cutoff; make it a setting if anyone ever asks.
	prewarmRecencyCutoff = 14 * 24 * time.Hour
)

// prewarmCandidate is the minimal per-show data needed to pick prewarm targets.
// Kept free of anilist types so selectPrewarmTargets is a pure, unit-testable function.
type prewarmCandidate struct {
	mediaId   int
	progress  int
	epCount   int       // current available episode count, -1 if unknown
	updated   time.Time // last watched, for recency ordering
	releasing bool      // show is currently airing (for the just-aired axis)
}

// prewarmTarget is a selected show + the next episode number to prewarm.
type prewarmTarget struct {
	mediaId int
	nextEp  int
}

// selectPrewarmTargets picks next-up episodes to prewarm, from two axes (pure function, no I/O):
//  1. the `maxWatched` most-recently-watched in-progress shows — the resume signal;
//  2. up to `maxJustAired` currently-RELEASING shows whose latest episode just became available
//     (progress+1 == epCount) and aren't already picked by axis 1 — so a new episode of a show you
//     follow gets prewarmed even if you didn't watch it in the last few sessions.
//
// Axis-1 order is preserved first, so opts[0] stays the most-recent watched (the tier-1 metadata
// target). progress is used only to pick which episode, never as a ranking signal.
func selectPrewarmTargets(cands []prewarmCandidate, maxWatched, maxJustAired int) []prewarmTarget {
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].updated.After(cands[j].updated) // most recent first
	})

	targets := make([]prewarmTarget, 0, maxWatched+maxJustAired)
	picked := make(map[int]bool)

	// Axis 1: most-recently-watched in-progress shows.
	for _, c := range cands {
		if len(targets) >= maxWatched {
			break
		}
		if c.progress < 1 {
			continue // never started
		}
		nextEp := c.progress + 1
		if c.epCount > 0 && nextEp > c.epCount {
			continue // caught up to what's aired/available
		}
		targets = append(targets, prewarmTarget{mediaId: c.mediaId, nextEp: nextEp})
		picked[c.mediaId] = true
	}

	// Axis 2: just-aired — releasing shows whose latest episode just dropped (one ahead of progress),
	// not already picked above. progress+1 == epCount precisely means "I'm caught up except the
	// newest", i.e. a fresh episode without needing air-time math.
	justAired := 0
	for _, c := range cands {
		if justAired >= maxJustAired {
			break
		}
		if picked[c.mediaId] || !c.releasing || c.progress < 1 {
			continue
		}
		nextEp := c.progress + 1
		if c.epCount <= 0 || nextEp != c.epCount {
			continue // only the just-available latest episode, not an older backlog ep
		}
		targets = append(targets, prewarmTarget{mediaId: c.mediaId, nextEp: nextEp})
		picked[c.mediaId] = true
		justAired++
	}
	return targets
}

// buildPrewarmCandidates cross-references the watch history (recency) with the AniList collection
// (progress, episode count) for shows the user is currently watching/repeating. The collection
// and continuity manager are the specific user's (per-user prewarm).
func (a *App) buildPrewarmCandidates(collection *anilist.AnimeCollection, cont *continuity.Manager) ([]prewarmCandidate, map[int]*anilist.BaseAnime) {
	if collection == nil || collection.GetMediaListCollection() == nil || cont == nil {
		return nil, nil
	}

	type entryInfo struct {
		progress  int
		epCount   int
		releasing bool
		media     *anilist.BaseAnime
	}
	entries := make(map[int]entryInfo)
	for _, list := range collection.GetMediaListCollection().GetLists() {
		if list == nil || list.GetStatus() == nil {
			continue
		}
		st := *list.GetStatus()
		if st != anilist.MediaListStatusCurrent && st != anilist.MediaListStatusRepeating {
			continue
		}
		for _, e := range list.GetEntries() {
			m := e.GetMedia()
			if m == nil {
				continue
			}
			progress := 0
			if e.GetProgress() != nil {
				progress = *e.GetProgress()
			}
			releasing := m.GetStatus() != nil && *m.GetStatus() == anilist.MediaStatusReleasing
			entries[m.GetID()] = entryInfo{progress: progress, epCount: m.GetCurrentEpisodeCount(), releasing: releasing, media: m}
		}
	}

	history := cont.GetWatchHistory()
	cands := make([]prewarmCandidate, 0, len(history))
	mediaById := make(map[int]*anilist.BaseAnime, len(history))
	for mediaId, item := range history {
		info, ok := entries[mediaId]
		if !ok {
			continue // not in current/repeating list (finished, dropped, etc.)
		}
		if time.Since(item.TimeUpdated) > prewarmRecencyCutoff {
			continue // user hasn't touched this in weeks — stop re-resolving it every tick
		}
		cands = append(cands, prewarmCandidate{
			mediaId:   mediaId,
			progress:  info.progress,
			epCount:   info.epCount,
			updated:   item.TimeUpdated,
			releasing: info.releasing,
		})
		mediaById[mediaId] = info.media
	}
	return cands, mediaById
}

// prewarmContinueWatchingStreams resolves and caches the next-up episode of the last few shows the
// user watched, so hitting play starts instantly. Gated by debrid being configured + PreloadNextStream.
// Quality-first selection is untouched: this only resolves the already-chosen auto-select result early.
func (a *App) prewarmContinueWatchingStreams() {
	defer util.HandlePanicInModuleThen("core/prewarmContinueWatchingStreams", func() {})

	if a.DebridClientRepository == nil || !a.DebridClientRepository.HasProvider() {
		return
	}
	settings := a.DebridClientRepository.GetSettings()
	if settings == nil || !settings.PreloadNextStream {
		return
	}

	// Drop expired shared prewarm rows on the tick (cheap; the table is small).
	a.DebridClientRepository.SweepExpiredPrewarms()

	// Per-user prewarm: the admin plus every active per-user session. Each user's next-up
	// episodes are resolved from THEIR collection + continuity and cached in THEIR stream
	// manager (opts.UserID), so prewarm data and cache are never shared across users.
	seen := make(map[uint]bool)
	var sessions []*UserSession
	addSession := func(s *UserSession) {
		if s == nil || seen[s.UserID] {
			return
		}
		seen[s.UserID] = true
		sessions = append(sessions, s)
	}
	addSession(a.SessionFor(a.adminUserID()))
	a.sessions.Range(func(_ uint, s *UserSession) bool {
		addSession(s)
		return true
	})

	var allOpts []*debrid_client.StartStreamOptions
	for _, s := range sessions {
		allOpts = append(allOpts, a.buildPrewarmOptsForSession(s)...)
	}
	if len(allOpts) == 0 {
		return
	}

	a.Logger.Debug().Int("count", len(allOpts)).Int("users", len(sessions)).Msg("app: Prewarming continue-watching streams")
	a.DebridClientRepository.PrewarmStreams(context.Background(), allOpts)
}

// buildPrewarmOptsForSession resolves one user's continue-watching prewarm targets from
// their own collection + continuity, tagging each with their UserID so it caches in their
// own stream manager.
func (a *App) buildPrewarmOptsForSession(s *UserSession) []*debrid_client.StartStreamOptions {
	if s == nil {
		return nil
	}
	collection, err := s.GetAnimeCollection(false)
	if err != nil {
		return nil
	}
	cands, mediaById := a.buildPrewarmCandidates(collection, s.Continuity())
	// Progress-aware cleanup: drop prewarm state (memory + shared DB rows) for episodes this
	// user has already watched. keepFromEp = progress-1 (the "n-2 rule"): keep the last-watched
	// episode AND its predecessor — AniList progress syncs at ~80% of an episode, so a plain
	// <progress cutoff deleted the previous episode's cache minutes into watching the next one.
	for _, c := range cands {
		if c.progress > 1 {
			a.DebridClientRepository.CleanupWatchedPrewarms(s.UserID, c.mediaId, c.progress-1)
		}
	}
	targets := selectPrewarmTargets(cands, prewarmContinueWatchingCount, prewarmJustAiredCount)
	if len(targets) == 0 {
		return nil
	}

	opts := make([]*debrid_client.StartStreamOptions, 0, len(targets))
	for _, t := range targets {
		media := mediaById[t.mediaId]
		if media == nil {
			continue
		}
		// Resolve the real AniDB episode so the cache key matches what the client sends at play time
		// (differs from strconv(nextEp) for shows with specials / multiple seasons).
		aniDBEpisode := strconv.Itoa(t.nextEp)
		if ec, err := anime.NewEpisodeCollection(anime.NewEpisodeCollectionOptions{
			Media:               media,
			MetadataProviderRef: a.MetadataProviderRef,
			Logger:              a.Logger,
		}); err == nil {
			if ep, ok := ec.FindEpisodeByNumber(t.nextEp); ok && ep.AniDBEpisode != "" {
				aniDBEpisode = ep.AniDBEpisode
			}
		}
		opts = append(opts, &debrid_client.StartStreamOptions{
			MediaId:       t.mediaId,
			EpisodeNumber: t.nextEp,
			AniDBEpisode:  aniDBEpisode,
			UserID:        s.UserID,
			AutoSelect:    true,
			Preload:       true,
			// Priority: this is the continue-watching next-up set the user actually clicks — it must
			// survive the speculative browse/search/discover hover firehose (uncapped, not evicted).
			Priority: true,
			// Default URL-only; the tier-1 target (most-recent show, set after the loop) also
			// pre-parses MKV metadata + warms the CDN. Keeping it to ONE target/user (was 3) + the
			// prewarm queue + the directstream CDN-warm limiter is what makes this safe — the
			// per-user×3 simultaneous font fan-out is what 429'd the CDN before.
			PrewarmMetadata: false,
		})
	}
	// Tier-1 metadata prewarm (M1/M2): opts are recency-ordered (selectPrewarmTargets sorts
	// most-recent first), so opts[0] is the show the user is most likely to resume next. Enable the
	// full metadata-parse + CDN-warm for it alone — the "instant first frame" win without the burst.
	if len(opts) > 0 {
		opts[0].PrewarmMetadata = true
	}
	return opts
}

// startContinueWatchingPrewarmLoop runs the prewarm on a low-frequency ticker. Each tick is a cheap
// no-op when preload is off / no debrid provider, so it's safe to start unconditionally.
func (a *App) startContinueWatchingPrewarmLoop() {
	go func() {
		defer util.HandlePanicInModuleThen("core/startContinueWatchingPrewarmLoop", func() {})
		time.Sleep(prewarmInitialDelay)
		a.prewarmContinueWatchingStreams()
		ticker := time.NewTicker(prewarmInterval)
		defer ticker.Stop()
		for range ticker.C {
			a.prewarmContinueWatchingStreams()
		}
	}()
}
