package handlers

import (
	"errors"
	"seanime/internal/api/anilist"
	"seanime/internal/library/anime"
	"seanime/internal/util/limiter"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/lo"
)

// franchiseRootTitle returns a media's preferred romaji-ish title for franchise stemming.
func franchiseRootTitle(m *anilist.BaseAnime) string {
	t := m.GetTitle()
	if t == nil {
		return ""
	}
	if t.GetRomaji() != nil {
		return *t.GetRomaji()
	}
	if t.GetUserPreferred() != nil {
		return *t.GetUserPreferred()
	}
	if t.GetEnglish() != nil {
		return *t.GetEnglish()
	}
	return ""
}

// extraRelationTypes are the relation kinds (beyond sequel/prequel, which the tree
// walk already follows) under which franchise movies/OVAs/specials are linked.
var extraRelationTypes = map[anilist.MediaRelation]bool{
	anilist.MediaRelationSideStory: true,
	anilist.MediaRelationSpinOff:   true,
	anilist.MediaRelationParent:    true,
	anilist.MediaRelationSummary:   true,
}

// HandleGetAnimeFranchise
//
//	@summary returns the franchise (seasons + extras + watch order) for an AniList anime media id.
//	@desc Presentation-only season grouping (Stremio-style). Walks the SEQUEL/PREQUEL
//	@desc relation tree, resolves each member's TMDB id + season number, and returns one
//	@desc grouped anime.FranchiseGroup. Tracking is untouched — each season stays its own
//	@desc AniList entry.
//	@route /api/v1/library/anime-entry/{id}/franchise [GET]
//	@param id - int - true - "AniList anime media ID"
//	@returns anime.FranchiseGroup
func (h *Handler) HandleGetAnimeFranchise(c echo.Context) error {
	mId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return h.RespondWithError(c, err)
	}
	group, err := h.resolveFranchiseGroup(c, mId)
	if err != nil {
		return h.RespondWithError(c, err)
	}
	// Optionally drop spin-offs and/or recaps from the dropdown. Filtered post-cache (on a
	// copy) so toggling a setting takes effect immediately without invalidating the cache.
	if s, e := h.App.Database.GetSettings(); e == nil && s.Library != nil {
		hide := map[string]bool{}
		if s.Library.HideFranchiseSpinoffs {
			hide["SPIN_OFF"] = true
		}
		if s.Library.HideFranchiseRecaps {
			hide["SUMMARY"] = true
		}
		if len(hide) > 0 {
			group = filterFranchiseRelations(group, hide)
		}
	}
	return h.RespondWithData(c, group)
}

// filterFranchiseRelations returns a shallow copy of the group with extras whose
// RelationType is in `hide` removed from Extras + WatchOrder (Seasons are never extras).
// Does not mutate the cached group.
func filterFranchiseRelations(g *anime.FranchiseGroup, hide map[string]bool) *anime.FranchiseGroup {
	if g == nil {
		return g
	}
	keep := func(e *anime.GroupedEntry, _ int) bool { return e == nil || !hide[e.RelationType] }
	cp := *g
	cp.Extras = lo.Filter(g.Extras, keep)
	cp.WatchOrder = lo.Filter(g.WatchOrder, keep)
	return &cp
}

// resolveFranchiseGroup walks the franchise relation tree (or returns the cached
// group) for the given root media id. Shared by the franchise + merged-season routes.
func (h *Handler) resolveFranchiseGroup(c echo.Context, mId int) (*anime.FranchiseGroup, error) {
	// Cache hit avoids the expensive relation walk entirely.
	if g, ok := anime.GetCachedFranchiseGroup(h.App.FileCacher, mId); ok {
		return g, nil
	}

	client := h.App.AnilistClientRef.Get()

	// Walk the franchise's SEQUEL/PREQUEL relation tree (the season spine).
	rl := limiter.NewLimiter(time.Second, 20)
	tree := anilist.NewCompleteAnimeRelationTree()
	cache := anilist.NewCompleteAnimeCache()
	root := &anilist.BaseAnime{ID: mId}
	_ = root.FetchMediaTree(anilist.FetchMediaTreeAll, client, rl, tree, cache)

	members := make([]*anilist.BaseAnime, 0)
	seen := make(map[int]bool)
	relationOf := make(map[int]string) // member id -> AniList relation to the main line
	addMember := func(m *anilist.BaseAnime, rel string) {
		if m == nil || m.GetID() == 0 || seen[m.GetID()] {
			return // first add wins: main-line members keep their "" relation
		}
		seen[m.GetID()] = true
		members = append(members, m)
		relationOf[m.GetID()] = rel
	}

	// Seasons (the sequel/prequel spine) — main line, no tag.
	tree.Range(func(_ int, v *anilist.CompleteAnime) bool {
		addMember(v.ToBaseAnime(), "")
		return true
	})

	// Extras: side-stories / spin-offs / movies / OVAs / specials linked to a member.
	// Read straight from already-fetched relation edges — no extra calls. Any format is
	// allowed (a side-story TV is an extra, not a season); the relation type drives its tag.
	tree.Range(func(_ int, v *anilist.CompleteAnime) bool {
		for _, edge := range v.GetRelations().GetEdges() {
			node := edge.GetNode()
			if node == nil || node.GetFormat() == nil || edge.GetRelationType() == nil {
				continue
			}
			rel := edge.GetRelationType().String()
			if !extraRelationTypes[*edge.GetRelationType()] {
				continue
			}
			if node.GetStatus() != nil && *node.GetStatus() == anilist.MediaStatusNotYetReleased {
				continue
			}
			addMember(node, rel)
		}
		return true
	})

	// Fall back to the single requested media if the relation walk yielded nothing.
	if len(members) == 0 {
		res, e := client.CompleteAnimeByID(c.Request().Context(), &mId)
		if e != nil {
			return nil, e
		}
		addMember(res.GetMedia().ToBaseAnime(), "")
	}

	resolver := anime.NewFranchiseResolver(h.App.MetadataProviderRef.Get(), h.App.FileCacher, h.App.Logger)
	refs := resolver.ResolveMany(lo.Map(members, func(m *anilist.BaseAnime, _ int) int { return m.GetID() }))

	// Bound the relation walk: the SEQUEL/PREQUEL chain can bridge into a genuinely
	// different show (e.g. Initial D -> MF Ghost). For TV members, drop ones that are
	// a different show by TMDB id, OR (when TMDB is missing on either side) whose title
	// stem doesn't overlap the root's. Movies/OVAs are extras and always kept.
	rootTmdb := refs[mId].TmdbId
	rootStem := ""
	for _, m := range members {
		if m.GetID() == mId {
			rootStem = anime.FranchiseTitleStem(franchiseRootTitle(m))
			break
		}
	}
	members = lo.Filter(members, func(m *anilist.BaseAnime, _ int) bool {
		if relationOf[m.GetID()] != "" {
			return true // side-stories / spin-offs are extras — keep regardless of TMDB
		}
		f := m.GetFormat()
		isTV := f != nil && (*f == anilist.MediaFormatTv || *f == anilist.MediaFormatTvShort || *f == anilist.MediaFormatOna)
		if !isTV {
			return true // movies / OVAs are extras
		}
		memTmdb := refs[m.GetID()].TmdbId
		if memTmdb != "" && rootTmdb != "" {
			return memTmdb == rootTmdb // both known: keep only if same show
		}
		// TMDB unknown on one side: fall back to title-stem overlap.
		if rootStem == "" {
			return true
		}
		memStem := anime.FranchiseTitleStem(franchiseRootTitle(m))
		return anime.FranchiseStemsOverlap(memStem, rootStem)
	})

	group := anime.BuildFranchiseFromMembers(members, refs, relationOf)
	anime.CacheFranchiseGroup(h.App.FileCacher, group)

	return group, nil
}

// HandleGetMergedSeason
//
//	@summary returns a split-cour season merged into one continuous episode list.
//	@desc For a season made of multiple AniList entries (cours) sharing a TMDB season
//	@desc number, concatenates their episode collections into one list. Each episode keeps
//	@desc its source cour media id + cour-relative number (for per-cour AniList progress)
//	@desc and absolute number (for batch/torrent matching). UI shows a continuous count;
//	@desc AniList stays tracked per cour.
//	@route /api/v1/library/anime-entry/{id}/merged-season/{season} [GET]
//	@returns anime.MergedSeason
func (h *Handler) HandleGetMergedSeason(c echo.Context) error {
	mId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return h.RespondWithError(c, err)
	}
	seasonNum, err := strconv.Atoi(c.Param("season"))
	if err != nil {
		return h.RespondWithError(c, err)
	}

	group, err := h.resolveFranchiseGroup(c, mId)
	if err != nil {
		return h.RespondWithError(c, err)
	}

	// Optional TMDB filter distinguishes real cours of a season from siblings that
	// metadata mislabeled with the same season number (e.g. Ascendance's unreleased
	// "Adopted Daughter", which has the same season number but a different/empty TMDB).
	tmdb := c.QueryParam("tmdb")
	cours := lo.Filter(group.Seasons, func(e *anime.GroupedEntry, _ int) bool {
		return e.SeasonNumber == seasonNum && e.Media != nil && (tmdb == "" || e.TmdbId == tmdb)
	})
	if len(cours) == 0 {
		return h.RespondWithError(c, errors.New("no cours found for that season"))
	}

	animeCollection, _ := h.App.GetAnimeCollection(false)

	merged := &anime.MergedSeason{SeasonNumber: seasonNum, Cours: []*anime.MergedCour{}, Episodes: []*anime.Episode{}}
	globalEp := 0
	for _, cour := range cours {
		ec, e := anime.NewEpisodeCollection(anime.NewEpisodeCollectionOptions{
			Media:               cour.Media,
			MetadataProviderRef: h.App.MetadataProviderRef,
			Logger:              h.App.Logger,
		})
		if e != nil || ec == nil {
			continue
		}

		mc := &anime.MergedCour{MediaId: cour.MediaId, Media: cour.Media, StartEpisode: globalEp + 1}
		if animeCollection != nil {
			mc.Progress = franchiseEntryProgress(animeCollection, cour.MediaId)
		}
		for _, ep := range ec.Episodes {
			if ep.Type != anime.LocalFileTypeMain {
				continue
			}
			merged.Episodes = append(merged.Episodes, ep)
			globalEp++
			mc.EpisodeCount++
		}
		merged.Cours = append(merged.Cours, mc)
		merged.TotalProgress += mc.Progress
	}
	merged.TotalEpisodes = globalEp

	return h.RespondWithData(c, merged)
}

// franchiseEntryProgress returns the user's AniList progress for a media id, 0 if absent.
func franchiseEntryProgress(col *anilist.AnimeCollection, mediaId int) int {
	if col == nil || col.MediaListCollection == nil {
		return 0
	}
	for _, l := range col.MediaListCollection.Lists {
		for _, e := range l.GetEntries() {
			if e != nil && e.Media != nil && e.Media.ID == mediaId {
				return e.GetProgressSafe()
			}
		}
	}
	return 0
}

// HandleGetFranchiseRefs
//
//	@summary resolves franchise grouping refs (TMDB id + season number) for many AniList ids.
//	@desc Cheap bulk lookup (metadata only, no relation walk) used to collapse the library
//	@desc into one card per franchise. Heavily cached per id.
//	@route /api/v1/library/franchise-refs [POST]
//	@returns []anime.FranchiseRefEntry
func (h *Handler) HandleGetFranchiseRefs(c echo.Context) error {
	type body struct {
		MediaIds []int `json:"mediaIds"`
	}
	var b body
	if err := c.Bind(&b); err != nil {
		return h.RespondWithError(c, err)
	}

	resolver := anime.NewFranchiseResolver(h.App.MetadataProviderRef.Get(), h.App.FileCacher, h.App.Logger)
	refs := resolver.ResolveMany(b.MediaIds)

	out := make([]anime.FranchiseRefEntry, 0, len(refs))
	for id, r := range refs {
		out = append(out, anime.FranchiseRefEntry{MediaId: id, TmdbId: r.TmdbId, SeasonNumber: r.SeasonNumber})
	}

	return h.RespondWithData(c, out)
}
