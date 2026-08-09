package util

import (
	"reflect"
	"testing"
)

func TestDisplayLanguagesFromFlags(t *testing.T) {
	gb := "\U0001F1EC\U0001F1E7" // 🇬🇧
	jp := "\U0001F1EF\U0001F1F5" // 🇯🇵
	// Real SeaDex layout: flags only, repeated, no language text.
	got := DisplayLanguagesFromFlags("SeaDex 1080p (Best) BluRay HEVC FLAC AAC " + gb + " / " + jp + " " + gb)
	want := []string{"English", "Japanese"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLanguagesFromFlags = %v, want %v", got, want)
	}
	if len(DisplayLanguagesFromFlags("No flags here 1080p BluRay")) != 0 {
		t.Fatalf("expected no languages when there are no flags")
	}
}

// AIOStreams writes "🌐 <audio flags>📝 <subtitle flags>". Subtitle flags must not be decoded as
// audio languages: "🇯🇵📝 🇬🇧" is a Japanese-audio release with English subs, not an English dub.
func TestLanguagesFromFlags_SubtitleSegmentExcluded(t *testing.T) {
	gb := "\U0001F1EC\U0001F1E7" // 🇬🇧
	jp := "\U0001F1EF\U0001F1F5" // 🇯🇵
	fr := "\U0001F1EB\U0001F1F7" // 🇫🇷
	memo := "\U0001F4DD"         // 📝

	if got, want := LanguagesFromFlags("Nisekoi E17 BluRay smol "+jp+memo+" "+gb), []string{"jp", "jpn", "ja", "japanese"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("LanguagesFromFlags = %v, want %v", got, want)
	}
	if got, want := DisplayLanguagesFromFlags("Iruma-kun E11 "+gb+" / "+jp+memo+" "+gb+" / "+fr), []string{"English", "Japanese"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DisplayLanguagesFromFlags = %v, want %v", got, want)
	}
}

func TestMergeLanguages(t *testing.T) {
	got := MergeLanguages([]string{"Japanese"}, []string{"japanese", "English"})
	want := []string{"Japanese", "English"} // case-insensitive dedupe, order preserved
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeLanguages = %v, want %v", got, want)
	}
}
