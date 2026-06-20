package handlers

import (
	"seanime/internal/api/anilist"
	"seanime/internal/library/anime"
	"seanime/internal/util/limiter"
	"strconv"
	"strings"
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

	// Cache hit avoids the expensive relation walk entirely.
	if g, ok := anime.GetCachedFranchiseGroup(h.App.FileCacher, mId); ok {
		return h.RespondWithData(c, g)
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
	addMember := func(m *anilist.BaseAnime) {
		if m == nil || m.GetID() == 0 || seen[m.GetID()] {
			return
		}
		seen[m.GetID()] = true
		members = append(members, m)
	}

	// Seasons (the sequel/prequel spine).
	tree.Range(func(_ int, v *anilist.CompleteAnime) bool {
		addMember(v.ToBaseAnime())
		return true
	})

	// Extras: movies/OVAs/specials linked to a member via side-story/spin-off/etc.
	// These are read straight from already-fetched relation edges — no extra calls.
	tree.Range(func(_ int, v *anilist.CompleteAnime) bool {
		for _, edge := range v.GetRelations().GetEdges() {
			node := edge.GetNode()
			if node == nil || node.GetFormat() == nil || edge.GetRelationType() == nil {
				continue
			}
			if !extraRelationTypes[*edge.GetRelationType()] {
				continue
			}
			switch *node.GetFormat() {
			case anilist.MediaFormatMovie, anilist.MediaFormatOva, anilist.MediaFormatSpecial:
				if node.GetStatus() != nil && *node.GetStatus() == anilist.MediaStatusNotYetReleased {
					continue
				}
				addMember(node)
			}
		}
		return true
	})

	// Fall back to the single requested media if the relation walk yielded nothing.
	if len(members) == 0 {
		res, e := client.CompleteAnimeByID(c.Request().Context(), &mId)
		if e != nil {
			return h.RespondWithError(c, e)
		}
		addMember(res.GetMedia().ToBaseAnime())
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
		return memStem != "" && (strings.Contains(memStem, rootStem) || strings.Contains(rootStem, memStem))
	})

	group := anime.BuildFranchiseFromMembers(members, refs)
	anime.CacheFranchiseGroup(h.App.FileCacher, group)

	return h.RespondWithData(c, group)
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
