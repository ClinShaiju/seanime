package hibiketorrent

import "testing"

// TestIdentity guards the dedup key: URL-only results (no infohash) must stay distinct
// instead of collapsing to the same empty key, while infohash still wins when present.
func TestIdentity(t *testing.T) {
	cases := []struct {
		name string
		in   AnimeTorrent
		want string
	}{
		{"infohash wins", AnimeTorrent{InfoHash: "abc", StreamUrl: "http://x", Name: "n"}, "abc"},
		{"streamurl over name", AnimeTorrent{StreamUrl: "http://x", Name: "n"}, "http://x"},
		{"name fallback", AnimeTorrent{Name: "n"}, "n"},
		{"distinct urls", AnimeTorrent{Name: "same", StreamUrl: "http://a"}, "http://a"},
	}
	for _, c := range cases {
		if got := c.in.Identity(); got != c.want {
			t.Errorf("%s: Identity()=%q want %q", c.name, got, c.want)
		}
	}

	// Two results with the same name but different URLs must not dedup together.
	a := AnimeTorrent{Name: "Release 1080p", StreamUrl: "http://a/1"}
	b := AnimeTorrent{Name: "Release 1080p", StreamUrl: "http://b/2"}
	if a.Identity() == b.Identity() {
		t.Fatal("same-name URL-only results collapsed to one identity")
	}
}
