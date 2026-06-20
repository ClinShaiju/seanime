package anime

import (
	"seanime/internal/api/anilist"
	"seanime/internal/api/metadata"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestFranchiseTitleStem(t *testing.T) {
	require.Equal(t, "initial d", FranchiseTitleStem("Initial D"))
	require.Equal(t, "initial d", FranchiseTitleStem("Initial D Second Stage"))
	require.Equal(t, "initial d", FranchiseTitleStem("Initial D Final Stage"))
	require.Equal(t, "mf ghost", FranchiseTitleStem("MF Ghost 3rd Season"))
	require.Equal(t, "ascendance of a bookworm", FranchiseTitleStem("Ascendance of a Bookworm"))

	// MF Ghost is a different franchise from Initial D (no stem overlap).
	initd, mfg := FranchiseTitleStem("Initial D"), FranchiseTitleStem("MF Ghost 3rd Season")
	require.False(t, strings.Contains(initd, mfg) || strings.Contains(mfg, initd))

	// A subtitled continuation still overlaps its franchise root.
	root := FranchiseTitleStem("Ascendance of a Bookworm")
	sub := FranchiseTitleStem("Ascendance of a Bookworm: Adopted Daughter of an Archduke")
	require.True(t, strings.Contains(sub, root))
}

func TestFranchiseStemsOverlap(t *testing.T) {
	// Sibling seasons with different subtitles (romaji) share the franchise base.
	require.True(t, FranchiseStemsOverlap(
		FranchiseTitleStem("Honzuki no Gekokujou: Ryoushu no Youjo"),
		FranchiseTitleStem("Honzuki no Gekokujou: Shisho ni Naru Tame ni wa Shudan wo Erandeiraremasen"),
	))
	// Different shows don't overlap.
	require.False(t, FranchiseStemsOverlap(FranchiseTitleStem("MF Ghost 3rd Season"), FranchiseTitleStem("Initial D")))
	// Containment still works.
	require.True(t, FranchiseStemsOverlap("ascendance of a bookworm adopted daughter", "ascendance of a bookworm"))
}

func frMkMedia(id int, format anilist.MediaFormat, year, episodes int) *anilist.BaseAnime {
	return &anilist.BaseAnime{
		ID:        id,
		Format:    lo.ToPtr(format),
		Episodes:  lo.ToPtr(episodes),
		StartDate: &anilist.BaseAnime_StartDate{Year: lo.ToPtr(year)},
	}
}

func frIds(es []*GroupedEntry) []int {
	return lo.Map(es, func(e *GroupedEntry, _ int) int { return e.MediaId })
}

func TestGroupEntriesByFranchise(t *testing.T) {
	// Danmachi-like: 5 TV seasons + 1 OVA sharing the show's TMDB id, plus a
	// standalone movie with its own TMDB id.
	const showTmdb = "65779"
	s1 := frMkMedia(1, anilist.MediaFormatTv, 2015, 13)
	s2 := frMkMedia(2, anilist.MediaFormatTv, 2017, 13)
	s3 := frMkMedia(3, anilist.MediaFormatTv, 2019, 13)
	s4 := frMkMedia(4, anilist.MediaFormatTv, 2021, 13)
	s5 := frMkMedia(5, anilist.MediaFormatTv, 2023, 13)
	ova := frMkMedia(6, anilist.MediaFormatOva, 2018, 1)
	movie := frMkMedia(7, anilist.MediaFormatMovie, 2020, 1)

	refs := map[int]FranchiseRef{
		1: {TmdbId: showTmdb, SeasonNumber: 1},
		2: {TmdbId: showTmdb, SeasonNumber: 2},
		3: {TmdbId: showTmdb, SeasonNumber: 3},
		4: {TmdbId: showTmdb, SeasonNumber: 4},
		5: {TmdbId: showTmdb, SeasonNumber: 5},
		6: {TmdbId: showTmdb, SeasonNumber: 0}, // OVA = TMDB season 0 -> extra
		7: {TmdbId: "999", SeasonNumber: -1},   // movie has its own TMDB id
	}

	// Deliberately unsorted input.
	groups := GroupEntriesByFranchise([]*anilist.BaseAnime{s3, s1, ova, s5, movie, s2, s4}, refs)
	require.Len(t, groups, 2)

	show, ok := lo.Find(groups, func(g *FranchiseGroup) bool { return g.TmdbId == showTmdb })
	require.True(t, ok)
	require.Equal(t, []int{1, 2, 3, 4, 5}, frIds(show.Seasons), "seasons ordered by season number")
	require.Equal(t, []int{6}, frIds(show.Extras), "OVA bucketed as extra")
	require.Equal(t, 1, show.RootMediaId, "root = first season")
	require.Equal(t, []int{1, 2, 6, 3, 4, 5}, frIds(show.WatchOrder), "watch order by air date")

	mov, ok := lo.Find(groups, func(g *FranchiseGroup) bool { return g.TmdbId == "999" })
	require.True(t, ok)
	require.Empty(t, mov.Seasons)
	require.Equal(t, []int{7}, frIds(mov.Extras))
	require.Equal(t, 7, mov.RootMediaId, "root falls back to first extra")

	// Next watch: S1 fully watched, S2 partially -> S2 is next.
	next, ok := show.NextWatch(map[int]int{1: 13, 2: 5})
	require.True(t, ok)
	require.Equal(t, 2, next.MediaId)
}

func TestSeasonNumberFromMetadata(t *testing.T) {
	// First main episode carries the season.
	md := &metadata.AnimeMetadata{Episodes: map[string]*metadata.EpisodeMetadata{
		"1": {Episode: "1", SeasonNumber: 3},
	}}
	require.Equal(t, 3, seasonNumberFromMetadata(md))

	// No "1" episode: smallest positive season among numeric episodes, ignoring specials.
	md2 := &metadata.AnimeMetadata{Episodes: map[string]*metadata.EpisodeMetadata{
		"S1": {Episode: "S1", SeasonNumber: 0},
		"5":  {Episode: "5", SeasonNumber: 2},
		"6":  {Episode: "6", SeasonNumber: 2},
	}}
	require.Equal(t, 2, seasonNumberFromMetadata(md2))

	// Nothing usable.
	require.Equal(t, -1, seasonNumberFromMetadata(&metadata.AnimeMetadata{}))
}
