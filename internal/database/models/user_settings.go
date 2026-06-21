package models

// UserSettings stores a single user's overrides over the shared server Settings
// (multi-user profiles). It's a JSON blob (UserOverrides) rather than a column-by-column
// split, so the admin keeps owning the base Settings row and each user layers their own
// user-editable fields on top. Locked/admin fields are never part of UserOverrides, so a
// user can never override them.
type UserSettings struct {
	BaseModel
	UserID uint   `gorm:"column:user_id;uniqueIndex" json:"userId"`
	Value  []byte `gorm:"column:value" json:"-"` // JSON of UserOverrides
}

// UserOverrides is the set of settings a regular user may control for themselves. It is
// overlaid onto the server Settings to produce the user's effective settings. Keep this
// in sync with profile-settings-split-spec.md. (Per-device settings — video playback,
// media player, external player — are client-side and intentionally NOT here.)
type UserOverrides struct {
	// AniList view preferences.
	Anilist *AnilistSettings `json:"anilist,omitempty"`
	// Notification preferences.
	Notifications *NotificationSettings `json:"notifications,omitempty"`
	// Discord rich-presence preferences (acted on client-side).
	Discord *DiscordSettings `json:"discord,omitempty"`
	// Manga: default provider + progress; local source dir stays server/admin.
	MangaDefaultProvider    string `json:"mangaDefaultProvider"`
	MangaAutoUpdateProgress bool   `json:"mangaAutoUpdateProgress"`
	// Library: season-grouping (presentation) + per-user playback prefs. The default
	// playback / episode source stays admin (locked).
	GroupSeasons          bool `json:"groupSeasons"`
	HideFranchiseSpinoffs bool `json:"hideFranchiseSpinoffs"`
	HideFranchiseRecaps   bool `json:"hideFranchiseRecaps"`
	AutoUpdateProgress    bool `json:"autoUpdateProgress"`
	AutoPlayNextEpisode   bool `json:"autoPlayNextEpisode"`
	EnableWatchContinuity bool `json:"enableWatchContinuity"`
	// Debrid: when UseServerDebrid is false the user supplies their own provider+key
	// (the functional wiring — streaming through the user's debrid — lands with P4).
	UseServerDebrid    bool   `json:"useServerDebrid"`
	DebridProvider     string `json:"debridProvider"`
	DebridApiKey       string `json:"debridApiKey"`
}

// ApplyTo overlays these user overrides onto a (cloned) server Settings, producing the
// user's effective settings. The caller must pass a copy it owns — this mutates it.
func (o *UserOverrides) ApplyTo(s *Settings) {
	if o == nil || s == nil {
		return
	}
	if o.Anilist != nil {
		s.Anilist = o.Anilist
	}
	if o.Notifications != nil {
		s.Notifications = o.Notifications
	}
	if o.Discord != nil {
		s.Discord = o.Discord
	}
	if s.Manga != nil {
		s.Manga.DefaultProvider = o.MangaDefaultProvider
		s.Manga.AutoUpdateProgress = o.MangaAutoUpdateProgress
	}
	if s.Library != nil {
		s.Library.GroupSeasons = o.GroupSeasons
		s.Library.HideFranchiseSpinoffs = o.HideFranchiseSpinoffs
		s.Library.HideFranchiseRecaps = o.HideFranchiseRecaps
		s.Library.AutoUpdateProgress = o.AutoUpdateProgress
		s.Library.AutoPlayNextEpisode = o.AutoPlayNextEpisode
		s.Library.EnableWatchContinuity = o.EnableWatchContinuity
	}
}

// ExtractUserOverrides pulls the user-overridable fields out of a Settings into a fresh
// UserOverrides (used to seed a new user's overrides from the current effective settings).
func ExtractUserOverrides(s *Settings) *UserOverrides {
	o := &UserOverrides{UseServerDebrid: true}
	if s == nil {
		return o
	}
	o.Anilist = s.Anilist
	o.Notifications = s.Notifications
	o.Discord = s.Discord
	if s.Manga != nil {
		o.MangaDefaultProvider = s.Manga.DefaultProvider
		o.MangaAutoUpdateProgress = s.Manga.AutoUpdateProgress
	}
	if s.Library != nil {
		o.GroupSeasons = s.Library.GroupSeasons
		o.HideFranchiseSpinoffs = s.Library.HideFranchiseSpinoffs
		o.HideFranchiseRecaps = s.Library.HideFranchiseRecaps
		o.AutoUpdateProgress = s.Library.AutoUpdateProgress
		o.AutoPlayNextEpisode = s.Library.AutoPlayNextEpisode
		o.EnableWatchContinuity = s.Library.EnableWatchContinuity
	}
	return o
}
