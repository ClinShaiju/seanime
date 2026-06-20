package anime

import (
	"math"
	"regexp"
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata"
	"seanime/internal/api/metadata_provider"
	"seanime/internal/util/filecache"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/sourcegraph/conc/pool"
)

var franchiseStemStrip = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(the\s+)?final\s+(season|stage)\b`),
	regexp.MustCompile(`(?i)\b\d+(st|nd|rd|th)\s+(season|stage)\b`),
	regexp.MustCompile(`(?i)\b(second|third|fourth|fifth|sixth)\s+(season|stage)\b`),
	regexp.MustCompile(`(?i)\b(season|stage)\s*\d+\b`),
	regexp.MustCompile(`(?i)\bpart\s*\d+\b`),
	regexp.MustCompile(`(?i)\bcour\s*\d+\b`),
	regexp.MustCompile(`(?i)\s(ii|iii|iv|v|vi|vii)\b`),
}
var franchiseStemNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// FranchiseTitleStem strips trailing season/stage/part/ordinal markers so titles of
// the same franchise reduce to a shared stem (e.g. "Initial D Second Stage" -> "initial d").
// Used as a relation-walk boundary: a member whose stem doesn't overlap the root's is a
// different show (e.g. MF Ghost vs Initial D).
func FranchiseTitleStem(title string) string {
	t := " " + strings.ToLower(title) + " "
	for _, re := range franchiseStemStrip {
		t = re.ReplaceAllString(t, " ")
	}
	return strings.TrimSpace(franchiseStemNonAlnum.ReplaceAllString(t, " "))
}

// Season-select grouping (Stremio-style). Presentation-only overlay over AniList:
// each season stays its own AniList entry underneath, so tracking is unchanged.
// Grouping key = shared TMDB id (from animap, via the metadata provider); seasons
// are ordered by the per-episode SeasonNumber. See season-select-support.md.

type (
	// FranchiseRef is the per-AniList-entry grouping data resolved from metadata.
	FranchiseRef struct {
		TmdbId       string `json:"tmdbId"`
		SeasonNumber int    `json:"seasonNumber"` // -1 if unknown
	}

	// FranchiseRefEntry is a FranchiseRef tagged with its media id, for bulk
	// responses (the library-collapse path resolves many ids at once).
	FranchiseRefEntry struct {
		MediaId      int    `json:"mediaId"`
		TmdbId       string `json:"tmdbId"`
		SeasonNumber int    `json:"seasonNumber"`
	}

	// MergedSeason is a split-cour season presented as one continuous episode list.
	// Episodes keep their source cour BaseAnime + cour-relative number (AniList progress)
	// and absolute number (batch/torrent matching); the UI numbers them continuously.
	MergedSeason struct {
		SeasonNumber  int            `json:"seasonNumber"`
		Cours         []*MergedCour  `json:"cours"`
		Episodes      []*Episode     `json:"episodes"`
		TotalEpisodes int            `json:"totalEpisodes"`
		TotalProgress int            `json:"totalProgress"`
	}

	// MergedCour describes one cour within a merged season.
	MergedCour struct {
		MediaId      int                `json:"mediaId"`
		Media        *anilist.BaseAnime `json:"media"`
		Progress     int                `json:"progress"`     // user's AniList progress for this cour
		EpisodeCount int                `json:"episodeCount"`
		StartEpisode int                `json:"startEpisode"` // 1-based continuous number where this cour begins
	}

	// GroupedEntry is a media placed within a franchise.
	GroupedEntry struct {
		Media        *anilist.BaseAnime `json:"media"`
		MediaId      int                `json:"mediaId"`
		TmdbId       string             `json:"tmdbId"`       // for distinguishing cours of the same season from mislabeled siblings
		SeasonNumber int                `json:"seasonNumber"` // -1 if unknown
		IsExtra      bool               `json:"isExtra"`      // movie/OVA/special (TMDB season 0 or non-TV format)
	}

	// FranchiseGroup is one show: its ordered seasons + extras + a flat watch order.
	FranchiseGroup struct {
		TmdbId      string             `json:"tmdbId,omitempty"`
		RootMediaId int                `json:"rootMediaId"`
		RootMedia   *anilist.BaseAnime `json:"rootMedia"`
		Seasons     []*GroupedEntry    `json:"seasons"`    // TV, sorted by season number
		Extras      []*GroupedEntry    `json:"extras"`     // movie/OVA/special, sorted by air date
		WatchOrder  []*GroupedEntry    `json:"watchOrder"` // seasons ∪ extras, sorted by air date
	}
)

// GroupEntriesByFranchise buckets a flat list of media into franchise groups.
// Entries sharing a non-empty TMDB id collapse into one group; entries without a
// TMDB id stay as singleton groups.
//
// ponytail: no-TMDB entries become singletons; the SEQUEL/PREQUEL relation-tree
// fallback (FetchMediaTree) is the upgrade path if singletons prove too lossy.
func GroupEntriesByFranchise(medias []*anilist.BaseAnime, refs map[int]FranchiseRef) []*FranchiseGroup {
	order := make([]string, 0)
	buckets := make(map[string][]*GroupedEntry)
	tmdbOf := make(map[string]string)

	for _, m := range medias {
		if m == nil {
			continue
		}
		ref := refs[m.GetID()]
		key := "id:" + strconv.Itoa(m.GetID()) // singleton fallback
		if ref.TmdbId != "" {
			key = "tmdb:" + ref.TmdbId
		}
		if _, ok := buckets[key]; !ok {
			order = append(order, key)
			tmdbOf[key] = ref.TmdbId
		}
		buckets[key] = append(buckets[key], &GroupedEntry{
			Media:        m,
			MediaId:      m.GetID(),
			TmdbId:       ref.TmdbId,
			SeasonNumber: ref.SeasonNumber,
			IsExtra:      isExtra(m, ref.SeasonNumber),
		})
	}

	groups := make([]*FranchiseGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, buildFranchiseGroup(tmdbOf[key], buckets[key]))
	}
	return groups
}

// BuildFranchiseFromMembers builds one franchise group from a set of related media
// (e.g. a SEQUEL/PREQUEL relation tree). Unlike GroupEntriesByFranchise it does NOT
// bucket by TMDB id — the caller has already established these media are one
// franchise — so cross-TMDB movies/OVAs land in Extras of the same group.
func BuildFranchiseFromMembers(medias []*anilist.BaseAnime, refs map[int]FranchiseRef) *FranchiseGroup {
	entries := make([]*GroupedEntry, 0, len(medias))
	for _, m := range medias {
		if m == nil {
			continue
		}
		ref := refs[m.GetID()]
		entries = append(entries, &GroupedEntry{
			Media:        m,
			MediaId:      m.GetID(),
			TmdbId:       ref.TmdbId,
			SeasonNumber: ref.SeasonNumber,
			IsExtra:      isExtra(m, ref.SeasonNumber),
		})
	}
	g := buildFranchiseGroup("", entries)
	if g.RootMediaId != 0 {
		g.TmdbId = refs[g.RootMediaId].TmdbId
	}
	return g
}

func buildFranchiseGroup(tmdbId string, entries []*GroupedEntry) *FranchiseGroup {
	seasons := make([]*GroupedEntry, 0, len(entries))
	extras := make([]*GroupedEntry, 0)
	for _, e := range entries {
		if e.IsExtra {
			extras = append(extras, e)
		} else {
			seasons = append(seasons, e)
		}
	}

	// Seasons: by season number asc (unknown -1 sorts last), tiebreak air date asc.
	sort.SliceStable(seasons, func(i, j int) bool {
		si, sj := seasons[i].SeasonNumber, seasons[j].SeasonNumber
		if si != sj {
			if si < 0 {
				return false
			}
			if sj < 0 {
				return true
			}
			return si < sj
		}
		return mediaDateKey(seasons[i].Media) < mediaDateKey(seasons[j].Media)
	})

	// Extras + watch order: air date asc (locked: watch order = air date).
	sort.SliceStable(extras, func(i, j int) bool {
		return mediaDateKey(extras[i].Media) < mediaDateKey(extras[j].Media)
	})

	watch := make([]*GroupedEntry, 0, len(seasons)+len(extras))
	watch = append(watch, seasons...)
	watch = append(watch, extras...)
	sort.SliceStable(watch, func(i, j int) bool {
		return mediaDateKey(watch[i].Media) < mediaDateKey(watch[j].Media)
	})

	g := &FranchiseGroup{
		TmdbId:     tmdbId,
		Seasons:    seasons,
		Extras:     extras,
		WatchOrder: watch,
	}
	// Root = first season, else first extra.
	if len(seasons) > 0 {
		g.RootMediaId, g.RootMedia = seasons[0].MediaId, seasons[0].Media
	} else if len(extras) > 0 {
		g.RootMediaId, g.RootMedia = extras[0].MediaId, extras[0].Media
	}
	return g
}

// NextWatch returns the next entry to watch in air-date order, given per-media
// AniList progress (mediaId -> episodes watched). An entry counts as watched only
// when its episode count is known and progress has reached it.
func (g *FranchiseGroup) NextWatch(progress map[int]int) (*GroupedEntry, bool) {
	if g == nil {
		return nil, false
	}
	for _, e := range g.WatchOrder {
		eps := 0
		if e.Media != nil && e.Media.GetEpisodes() != nil {
			eps = *e.Media.GetEpisodes()
		}
		if eps <= 0 || progress[e.MediaId] < eps {
			return e, true
		}
	}
	return nil, false
}

// isExtra classifies a media as an extra (movie/OVA/special) rather than a season.
// TMDB season 0 is specials; a positive season is a real season; with no season
// number we fall back to the AniList format.
func isExtra(m *anilist.BaseAnime, season int) bool {
	// Format wins: movies/OVAs/specials are always extras, even when metadata gave
	// them a positive season number (e.g. Initial D Battle Stage OVAs tagged season 1).
	if f := m.GetFormat(); f != nil {
		switch *f {
		case anilist.MediaFormatMovie, anilist.MediaFormatOva, anilist.MediaFormatSpecial:
			return true
		}
	}
	// TV-like (or unknown format): TMDB season 0 = specials.
	return season == 0
}

// mediaDateKey returns a sortable YYYYMMDD int; unknown dates sort last.
func mediaDateKey(m *anilist.BaseAnime) int {
	if m == nil {
		return math.MaxInt32
	}
	y, mo, d := 0, 0, 0
	if sd := m.GetStartDate(); sd != nil {
		if sd.Year != nil {
			y = *sd.Year
		}
		if sd.Month != nil {
			mo = *sd.Month
		}
		if sd.Day != nil {
			d = *sd.Day
		}
	}
	if y == 0 {
		if sy := m.GetSeasonYear(); sy != nil {
			y = *sy
		}
	}
	if y == 0 {
		return math.MaxInt32
	}
	return y*10000 + mo*100 + d
}

// seasonNumberFromMetadata picks the entry's season number from animap episode
// data: the first main episode's season, else the smallest positive season among
// numeric episodes. -1 if none.
func seasonNumberFromMetadata(md *metadata.AnimeMetadata) int {
	if md == nil {
		return -1
	}
	if ep, ok := md.FindEpisode("1"); ok && ep.SeasonNumber > 0 {
		return ep.SeasonNumber
	}
	best := -1
	for key, ep := range md.Episodes {
		if len(key) == 0 || key[0] < '0' || key[0] > '9' {
			continue // skip specials ("S1", etc.)
		}
		if ep.SeasonNumber > 0 && (best == -1 || ep.SeasonNumber < best) {
			best = ep.SeasonNumber
		}
	}
	return best
}

//----------------------------------------------------------------------------------------------------------------------

var franchiseRefBucket = filecache.NewBucket("franchise-ref", 30*24*time.Hour)

// franchiseGroupBucket caches whole franchise groups. The relation-tree walk that
// builds a group is the expensive part (many AniList calls), so we cache the result
// under every member id — viewing any sibling season is then an instant cache hit
// and never re-hammers AniList. 24h TTL self-heals stale/partial walks.
var franchiseGroupBucket = filecache.NewBucket("franchise-group", 24*time.Hour)

// GetCachedFranchiseGroup returns a cached group for the given media id, if present.
func GetCachedFranchiseGroup(cacher *filecache.Cacher, mediaId int) (*FranchiseGroup, bool) {
	if cacher == nil {
		return nil, false
	}
	var g FranchiseGroup
	if ok, _ := cacher.Get(franchiseGroupBucket, strconv.Itoa(mediaId), &g); ok {
		return &g, true
	}
	return nil, false
}

// CacheFranchiseGroup stores the group under its root and every member id.
func CacheFranchiseGroup(cacher *filecache.Cacher, group *FranchiseGroup) {
	if cacher == nil || group == nil {
		return
	}
	seen := map[int]bool{}
	store := func(id int) {
		if id != 0 && !seen[id] {
			seen[id] = true
			_ = cacher.Set(franchiseGroupBucket, strconv.Itoa(id), group)
		}
	}
	store(group.RootMediaId)
	for _, e := range group.WatchOrder {
		store(e.MediaId)
	}
}

// FranchiseResolver resolves and caches FranchiseRef per AniList id using the
// metadata provider (which already fetches animap's TMDB id + season numbers).
type FranchiseResolver struct {
	provider metadata_provider.Provider
	cacher   *filecache.Cacher
	logger   *zerolog.Logger
}

func NewFranchiseResolver(provider metadata_provider.Provider, cacher *filecache.Cacher, logger *zerolog.Logger) *FranchiseResolver {
	return &FranchiseResolver{provider: provider, cacher: cacher, logger: logger}
}

// Resolve returns the franchise ref for one AniList id, reading from / filling the
// persistent cache. Transient metadata failures are not cached (so they retry).
func (r *FranchiseResolver) Resolve(id int) FranchiseRef {
	if r == nil {
		return FranchiseRef{SeasonNumber: -1}
	}
	if r.cacher != nil {
		var ref FranchiseRef
		if ok, _ := r.cacher.Get(franchiseRefBucket, strconv.Itoa(id), &ref); ok {
			return ref
		}
	}

	ref := FranchiseRef{SeasonNumber: -1}
	if r.provider != nil {
		md, err := r.provider.GetAnimeMetadata(metadata.AnilistPlatform, id)
		if err != nil || md == nil {
			return ref // don't cache a transient failure
		}
		ref.TmdbId = md.GetMappings().ThemoviedbId
		ref.SeasonNumber = seasonNumberFromMetadata(md)
	}

	if r.cacher != nil {
		_ = r.cacher.Set(franchiseRefBucket, strconv.Itoa(id), ref)
	}
	return ref
}

// ResolveMany resolves refs for many ids in parallel.
func (r *FranchiseResolver) ResolveMany(ids []int) map[int]FranchiseRef {
	out := make(map[int]FranchiseRef, len(ids))
	if r == nil {
		return out
	}
	p := pool.NewWithResults[lo.Tuple2[int, FranchiseRef]]().WithMaxGoroutines(8)
	for _, id := range ids {
		p.Go(func() lo.Tuple2[int, FranchiseRef] {
			return lo.T2(id, r.Resolve(id))
		})
	}
	for _, t := range p.Wait() {
		out[t.A] = t.B
	}
	return out
}
