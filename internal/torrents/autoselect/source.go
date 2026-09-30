package autoselect

import (
	"context"
	"regexp"
	hibiketorrent "seanime/internal/extension/hibike/torrent"
	"seanime/internal/util"
	"strings"

	"github.com/5rahim/habari"
)

// PreferredSource is the release a user last streamed for an entry. Auto-select keeps later
// episodes on it - the same pack, else the same release group at the same resolution - so a
// season doesn't hop between encodes from one episode to the next.
//
// A MANUAL pick outranks everything except the episode/season gates, until the user picks
// manually again. An AUTO pick ranks just under SeaDex: a curated release still wins when one
// exists, and the season then sticks to it.
type PreferredSource struct {
	Name       string `json:"name"`
	InfoHash   string `json:"infoHash,omitempty"`
	Group      string `json:"group,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Manual     bool   `json:"manual"`
}

func NewPreferredSource(t *hibiketorrent.AnimeTorrent, manual bool) *PreferredSource {
	if t == nil {
		return nil
	}
	return &PreferredSource{
		Name:       t.Name,
		InfoHash:   strings.ToLower(t.InfoHash),
		Group:      ReleaseGroup(t.Name),
		Resolution: strings.ToLower(t.Resolution),
		Manual:     manual,
	}
}

// Match rates how closely a release continues the preferred source: 2 = the same pack/torrent,
// 1 = the same release group (and resolution, when both are known), 0 = unrelated.
func (p *PreferredSource) Match(t *hibiketorrent.AnimeTorrent) int {
	if p == nil || t == nil {
		return 0
	}
	if p.InfoHash != "" && strings.EqualFold(p.InfoHash, t.InfoHash) {
		return 2
	}
	if p.Group == "" || !strings.EqualFold(p.Group, ReleaseGroup(t.Name)) {
		return 0
	}
	if res := strings.ToLower(t.Resolution); p.Resolution != "" && res != "" && res != p.Resolution {
		return 0
	}
	return 1
}

// releaseTagRe reads the group from an aggregator display name ("🎞️ AV1 🏷️ Breeze"), where the
// leading bracket is the debrid/addon tag ("[TB⚡] Debridio Scraper") and not a release group.
var releaseTagRe = regexp.MustCompile(`\x{1F3F7}\x{FE0F}?\s*([\p{L}\p{N}][\p{L}\p{N}._&-]*)`)

// ReleaseGroup returns a release's group in lower case, "" when unknown.
func ReleaseGroup(name string) string {
	if m := releaseTagRe.FindStringSubmatch(name); m != nil {
		return strings.ToLower(m[1])
	}
	return strings.ToLower(strings.TrimSpace(habari.Parse(util.CleanReleaseName(name)).ReleaseGroup))
}

type preferredSourceKey struct{}

// WithPreferredSource makes FindBestTorrent keep to the given source (nil = no preference).
func WithPreferredSource(ctx context.Context, p *PreferredSource) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, preferredSourceKey{}, p)
}

func preferredSourceFrom(ctx context.Context) *PreferredSource {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(preferredSourceKey{}).(*PreferredSource)
	return p
}
