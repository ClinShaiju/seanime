package core

import (
	"context"
	"seanime/internal/api/anilist"
	debrid_client "seanime/internal/debrid/client"
	"seanime/internal/library/anime"
	"seanime/internal/util"
	"sort"
	"strconv"
	"time"
)

const (
	prewarmContinueWatchingCount = 3                // next-up episode of the last N watched shows
	prewarmInitialDelay          = 30 * time.Second // let the collection + debrid settings load first
	prewarmInterval              = 10 * time.Minute // refresh before debrid URLs expire (cache TTL is 15m)
)

// prewarmCandidate is the minimal per-show data needed to pick prewarm targets.
// Kept free of anilist types so selectPrewarmTargets is a pure, unit-testable function.
type prewarmCandidate struct {
	mediaId  int
	progress int
	epCount  int       // current available episode count, -1 if unknown
	updated  time.Time // last watched, for recency ordering
}

// prewarmTarget is a selected show + the next episode number to prewarm.
type prewarmTarget struct {
	mediaId int
	nextEp  int
}

// selectPrewarmTargets picks the next-up episode for the `max` most-recently-watched shows that
// are still in progress (started, not caught up). Pure function — no I/O.
func selectPrewarmTargets(cands []prewarmCandidate, max int) []prewarmTarget {
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].updated.After(cands[j].updated) // most recent first
	})

	targets := make([]prewarmTarget, 0, max)
	for _, c := range cands {
		if len(targets) >= max {
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
	}
	return targets
}

// buildPrewarmCandidates cross-references the watch history (recency) with the AniList collection
// (progress, episode count) for shows the user is currently watching/repeating.
func (a *App) buildPrewarmCandidates() ([]prewarmCandidate, map[int]*anilist.BaseAnime) {
	collection, err := a.GetAnimeCollection(false)
	if err != nil || collection == nil || collection.GetMediaListCollection() == nil {
		return nil, nil
	}

	type entryInfo struct {
		progress int
		epCount  int
		media    *anilist.BaseAnime
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
			entries[m.GetID()] = entryInfo{progress: progress, epCount: m.GetCurrentEpisodeCount(), media: m}
		}
	}

	history := a.ContinuityManager.GetWatchHistory()
	cands := make([]prewarmCandidate, 0, len(history))
	mediaById := make(map[int]*anilist.BaseAnime, len(history))
	for mediaId, item := range history {
		info, ok := entries[mediaId]
		if !ok {
			continue // not in current/repeating list (finished, dropped, etc.)
		}
		cands = append(cands, prewarmCandidate{
			mediaId:  mediaId,
			progress: info.progress,
			epCount:  info.epCount,
			updated:  item.TimeUpdated,
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
	if a.ContinuityManager == nil {
		return
	}

	cands, mediaById := a.buildPrewarmCandidates()
	targets := selectPrewarmTargets(cands, prewarmContinueWatchingCount)
	if len(targets) == 0 {
		return
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
			AutoSelect:    true,
			Preload:       true,
			// Priority: this is the continue-watching next-up set the user actually clicks — it must
			// survive the speculative browse/search/discover hover firehose (uncapped, not evicted).
			Priority: true,
			// Also pre-parse metadata for continue-watching targets so launching one is fully
			// instant (URL + metadata + content-type), not just instant torrent selection. The
			// metadata parse downloads fonts to the SERVER (VPS↔CDN, fast) — not the client.
			PrewarmMetadata: true,
		})
	}

	if len(opts) == 0 {
		return
	}

	a.Logger.Debug().Int("count", len(opts)).Msg("app: Prewarming continue-watching streams")
	a.DebridClientRepository.PrewarmStreams(context.Background(), opts)
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
