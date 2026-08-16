package core

import (
	"errors"
	"seanime/internal/api/anilist"
	"testing"
)

// The auto-logout destroys the stored OAuth token, so a single AniList answer must never
// be able to condemn it on its own. Only an auth-shaped error is a vote for "dead".
func TestAnilistCheckSaysAlive(t *testing.T) {
	named := &anilist.GetViewer{Viewer: &anilist.GetViewer_Viewer{Name: "cvslinc"}}

	tests := []struct {
		name   string
		viewer *anilist.GetViewer
		err    error
		alive  bool
	}{
		{"named viewer", named, nil, true},
		{"invalid token", nil, errors.New(`{"graphqlErrors":[{"message":"Invalid token"}]}`), false},
		{"user not found", nil, errors.New("User not found"), false},
		{"network failure", nil, errors.New("dial tcp: i/o timeout"), true},
		{"server error", nil, errors.New("http status 502 Bad Gateway"), true},
		{"rate limited", nil, errors.New("http status 429 Too Many Requests"), true},
		{"empty response", nil, nil, false},
		{"nameless viewer", &anilist.GetViewer{Viewer: &anilist.GetViewer_Viewer{}}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anilistCheckSaysAlive(tt.viewer, tt.err); got != tt.alive {
				t.Errorf("anilistCheckSaysAlive() = %v, want %v", got, tt.alive)
			}
		})
	}
}
