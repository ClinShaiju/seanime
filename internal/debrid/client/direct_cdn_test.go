package debrid_client

import (
	"seanime/internal/database/models"
	"testing"
)

func TestDirectCdnEligibleWith(t *testing.T) {
	on := &models.DebridSettings{DirectCdnPlayback: true, Provider: "torbox"}
	capableNative := &StartStreamOptions{PlaybackType: PlaybackTypeNativePlayer, DirectCdnCapable: true}

	if !directCdnEligibleWith(on, capableNative) {
		t.Fatal("torbox + setting on + capable native player should be eligible")
	}

	// Every single gate flipped off must disable direct mode (proxy fallback).
	cases := []struct {
		name     string
		settings *models.DebridSettings
		opts     *StartStreamOptions
	}{
		{"nil settings", nil, capableNative},
		{"setting off", &models.DebridSettings{DirectCdnPlayback: false, Provider: "torbox"}, capableNative},
		{"provider not allowlisted", &models.DebridSettings{DirectCdnPlayback: true, Provider: "realdebrid"}, capableNative},
		{"client not capable", on, &StartStreamOptions{PlaybackType: PlaybackTypeNativePlayer, DirectCdnCapable: false}},
		{"not native player", on, &StartStreamOptions{PlaybackType: PlaybackTypeExternalPlayer, DirectCdnCapable: true}},
		{"nil opts", on, nil},
	}
	for _, c := range cases {
		if directCdnEligibleWith(c.settings, c.opts) {
			t.Fatalf("%s: should NOT be eligible", c.name)
		}
	}
}
