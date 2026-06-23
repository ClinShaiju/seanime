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

func TestMergeLanguages(t *testing.T) {
	got := MergeLanguages([]string{"Japanese"}, []string{"japanese", "English"})
	want := []string{"Japanese", "English"} // case-insensitive dedupe, order preserved
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeLanguages = %v, want %v", got, want)
	}
}
