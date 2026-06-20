package autoselect

import "testing"

func TestInfoHashFromMagnet(t *testing.T) {
	cases := map[string]string{
		"magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a&dn=Foo": "c12fe1c06bba254a9dc9f519b335aa7c1367a88a",
		"magnet:?dn=Foo&xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01":  "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		"magnet:?xt=urn:btih:deadbeef":                                        "deadbeef",
		"https://example.com/not-a-magnet":                                    "",
		"":                                                                    "",
	}
	for magnet, want := range cases {
		if got := infoHashFromMagnet(magnet); got != want {
			t.Errorf("infoHashFromMagnet(%q) = %q, want %q", magnet, got, want)
		}
	}
}
