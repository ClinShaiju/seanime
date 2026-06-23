package debrid_client

import "testing"

// Regression: AIOStreams marks TorBox-cached-but-not-instant results with a cloud+bolt
// prefix "[TB☁️⚡]" (vs the plain "[TB⚡]"). Both are cached. The cloud must not be read as
// an uncached marker. Real name string captured from the live aggregator.
func TestParseDebridCacheFlag_CloudBolt(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"[TB☁️⚡] SeaDex 1080p (Best)", true},  // cloud + bolt → cached
		{"[TB⚡] Nyaa.si 1080p", true},          // plain bolt → cached
		{"[TB⏳] SeaDex 1080p", false},          // hourglass → uncached
		{"[TB⬇] Some Release", false},           // down-arrow → uncached
	}
	for _, c := range cases {
		cached, known := parseDebridCacheFlag(c.name, "torbox")
		if !known {
			t.Fatalf("%q: expected a recognized flag, got known=false", c.name)
		}
		if cached != c.want {
			t.Fatalf("%q: cached=%v, want %v", c.name, cached, c.want)
		}
	}
}
