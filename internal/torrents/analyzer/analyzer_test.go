package torrent_analyzer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"seanime/internal/api/anilist"
	"seanime/internal/library/anime"
	"seanime/internal/platforms/platform"
	"seanime/internal/util"
)

func TestNewAnalyzerInitializesFiles(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "Season 1", "[Seanime] Example Show - 01.mkv"),
		filepath.Join(root, "Season 1", "[Seanime] Example Show - 02.mkv"),
	}
	media := &anilist.CompleteAnime{ID: 42}

	analyzer := NewAnalyzer(&NewAnalyzerOptions{
		Filepaths:   paths,
		Media:       media,
		ForceMatch:  true,
		PlatformRef: util.NewRef[platform.Platform](nil),
	})

	require.Len(t, analyzer.files, len(paths))
	require.Same(t, media, analyzer.media)
	require.True(t, analyzer.forceMatch)
	for index, file := range analyzer.files {
		require.Equal(t, index, file.GetIndex())
		require.Equal(t, filepath.ToSlash(paths[index]), file.GetPath())
		require.NotNil(t, file.GetLocalFile())
		require.Equal(t, filepath.ToSlash(paths[index]), file.GetLocalFile().Path)
	}
}

func TestAnalyzeTorrentFilesReturnsErrorWhenPlatformRefAbsent(t *testing.T) {
	analyzer := NewAnalyzer(&NewAnalyzerOptions{
		Filepaths: []string{filepath.Join(t.TempDir(), "[Seanime] Example Show - 01.mkv")},
		Media:     &anilist.CompleteAnime{ID: 42},
	})

	analysis, err := analyzer.AnalyzeTorrentFiles()

	require.Nil(t, analysis)
	require.EqualError(t, err, "anilist client wrapper is nil")
}

// Verifies that the helper methods for selecting files from the analysis work as expected
func TestAnalysisSelectionHelpers(t *testing.T) {
	analysis, files := newAnalysisFixture(t)

	correspondingFiles := analysis.GetCorrespondingFiles()
	require.Len(t, correspondingFiles, 3)
	require.Same(t, files[0], correspondingFiles[0])
	require.Same(t, files[1], correspondingFiles[1])
	require.Same(t, files[3], correspondingFiles[3])

	correspondingMainFiles := analysis.GetCorrespondingMainFiles()
	require.Len(t, correspondingMainFiles, 2)
	require.Same(t, files[0], correspondingMainFiles[0])
	require.Same(t, files[3], correspondingMainFiles[3])

	mainFile, ok := analysis.GetMainFileByEpisode(3)
	require.True(t, ok)
	require.Same(t, files[3], mainFile)

	missingMainFile, ok := analysis.GetMainFileByEpisode(99)
	require.False(t, ok)
	require.Nil(t, missingMainFile)

	aniDBFile, ok := analysis.GetFileByAniDBEpisode("3")
	require.True(t, ok)
	require.Same(t, files[3], aniDBFile)

	missingAniDBFile, ok := analysis.GetFileByAniDBEpisode("missing")
	require.False(t, ok)
	require.Nil(t, missingAniDBFile)

	unselectedFiles := analysis.GetUnselectedFiles()
	require.Len(t, unselectedFiles, 1)
	require.Same(t, files[2], unselectedFiles[2])

	require.ElementsMatch(t, []int{0, 3}, analysis.GetIndices(correspondingMainFiles))
	require.Equal(t, []int{1, 2}, analysis.GetUnselectedIndices(correspondingMainFiles))
	require.Equal(t, files, analysis.GetFiles())
}

func newAnalysisFixture(t *testing.T) (*Analysis, []*File) {
	t.Helper()
	root := t.TempDir()
	files := []*File{
		newAnalyzedFile(filepath.Join(root, "[Seanime] Example Show - 01.mkv"), 0, 42, 1, anime.LocalFileTypeMain, "1"),
		newAnalyzedFile(filepath.Join(root, "[Seanime] Example Show - OVA.mkv"), 1, 42, 0, anime.LocalFileTypeSpecial, "S1"),
		newAnalyzedFile(filepath.Join(root, "[Seanime] Other Show - 01.mkv"), 2, 7, 1, anime.LocalFileTypeMain, "1"),
		newAnalyzedFile(filepath.Join(root, "[Seanime] Example Show - 03.mkv"), 3, 42, 3, anime.LocalFileTypeMain, "3"),
	}

	return &Analysis{
		files: files,
		media: &anilist.CompleteAnime{ID: 42},
	}, files
}

func newAnalyzedFile(path string, index int, mediaID int, episode int, fileType anime.LocalFileType, aniDBEpisode string) *File {
	file := newFile(index, path)
	file.localFile.MediaId = mediaID
	file.localFile.Metadata = &anime.LocalFileMetadata{
		Episode:      episode,
		AniDBEpisode: aniDBEpisode,
		Type:         fileType,
	}
	return file
}

// Force-matching renumbers absolute files onto the requested cour, so a full-season pack has two
// files claiming each episode. Cour numbering decides, never file order.
func TestGetFileForEpisode_Cours(t *testing.T) {
	pack := func(names ...string) *Analysis {
		a := &Analysis{}
		for i, n := range names {
			f := newFile(i, n)
			f.localFile.Metadata = &anime.LocalFileMetadata{AniDBEpisode: "1"}
			a.files = append(a.files, f)
		}
		return a
	}
	pick := func(a *Analysis, c Cour) int {
		f, ok := a.GetFileForEpisode(1, c)
		if !ok {
			return -1
		}
		return f.GetIndex()
	}

	// Dr. Stone New World Part 2 episode 1 = S3 episode 12 (cour 1 has 11).
	drStone := pack(
		"Dr. STONE - New World/[sam] Dr. STONE - New World - 01v2 [BD 1080p FLAC] [A762B0C6].mkv",
		"Dr. STONE - New World/[sam] Dr. STONE - New World - 12 [BD 1080p FLAC] [0F48FCFD].mkv",
	)
	require.Equal(t, 1, pick(drStone, Cour{Season: 3, Index: 2, Offset: 11}), "continuous number")
	require.Equal(t, 0, pick(drStone, Cour{Season: 3, Index: 1}), "cour 1 keeps its own number")

	// Two 12-episode cours, no continuous file: the release labeled as cour 2.
	labeled := pack(
		"Show S2/[Grp] Show S2 - 01 [1080p].mkv",
		"Show S2 Part 2/[Grp] Show S2 Part 2 - 01 [1080p].mkv",
	)
	require.Equal(t, 1, pick(labeled, Cour{Season: 2, Index: 2, Offset: 12}), "cour-2 release")
	require.Equal(t, 0, pick(labeled, Cour{Season: 2, Index: 1}), "cour 1 skips the Part 2 file")

	// Nothing says which "01" is cour 2: skip the pack rather than guess.
	unlabeled := pack("A/[Grp] Show - 01 [1080p].mkv", "B/[Grp] Show - 01 [1080p].mkv")
	require.Equal(t, -1, pick(unlabeled, Cour{Season: 2, Index: 2, Offset: 12}))

	// A lone claimant is trusted unless it is labeled as another cour.
	require.Equal(t, 0, pick(pack("[Grp] Show S2 Part 2 - 01 [1080p].mkv"), Cour{Season: 2, Index: 2, Offset: 12}))
	require.Equal(t, -1, pick(pack("[Grp] Show S2 Part 1 - 01 [1080p].mkv"), Cour{Season: 2, Index: 2, Offset: 12}))
}
