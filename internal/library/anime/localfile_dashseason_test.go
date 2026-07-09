package anime

import "testing"

// TestNewLocalFile_DashSeasonEpisode verifies that "S<n>-<ep>" filenames yield the correct
// episode number after the pre-habari normalization, while real season ranges and plain
// episode ranges are left alone. Regression for [IFA1]_HnG!_S1-10.mkv (ep 10 was lost).
func TestNewLocalFile_DashSeasonEpisode(t *testing.T) {
	tests := []struct {
		name    string
		wantEp  string // expected parsed episode ("" = none)
		wantSea string // expected parsed single season ("" = none/range)
	}{
		{"[IFA1]_HnG!_S1-01.mkv", "01", "1"}, // leading-zero form already worked
		{"[IFA1]_HnG!_S1-09.mkv", "09", "1"},
		{"[IFA1]_HnG!_S1-10.mkv", "10", "1"}, // the broken case
		{"[IFA1]_HnG!_S1-13.mkv", "13", "1"},
		{"[Group] Show S2-5.mkv", "5", "2"},
		{"[Group] Show S1-S3 [Batch].mkv", "", ""}, // real season range: no single ep/season
	}
	for _, tt := range tests {
		lf := NewLocalFile(tt.name, "")
		if lf.ParsedData.Episode != tt.wantEp {
			t.Errorf("%q: episode = %q, want %q", tt.name, lf.ParsedData.Episode, tt.wantEp)
		}
		if lf.ParsedData.Season != tt.wantSea {
			t.Errorf("%q: season = %q, want %q", tt.name, lf.ParsedData.Season, tt.wantSea)
		}
	}
}
