package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"seanime/internal/api/anilist"
	"seanime/internal/continuity"
	"seanime/internal/database/db"
	"seanime/internal/directstream"
	"seanime/internal/events"
	"seanime/internal/library/playbackmanager"
	"seanime/internal/mediacore"
	"seanime/internal/mpvcore"
	"seanime/internal/nativeplayer"
	"seanime/internal/platforms/anilist_platform"
	"seanime/internal/platforms/platform"
	"seanime/internal/player"
	"seanime/internal/user"
	"seanime/internal/util"
	"seanime/internal/videocore"
	"sync"

	"github.com/goccy/go-json"
)

// userAnilistCacheDir returns a per-user AniList cache directory under the shared
// cache root. The AniList disk cache keys by query params only (not the token), so a
// shared directory would let one user's cached collection/lists/viewer be served to
// another (cross-user data bleed). Each non-admin user gets their own subdirectory.
// The admin keeps the root dir (single-tenant upgrades unchanged).
func userAnilistCacheDir(root string, userID uint) string {
	dir := filepath.Join(root, fmt.Sprintf("u%d", userID))
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

var errInvalidToken = errors.New("token is empty")

// UserSession bundles the per-user identity plane: a user's own AniList platform
// (their token + client) and the collection it serves. Shared engines (extensions,
// metadata, torrent/debrid/transcode) are reached through the App by reference, not
// copied per user.
//
// The admin (server owner) session is a thin delegate over the App's existing
// global platform and modules, so a single-user install behaves exactly as before —
// no extra platform is built, nothing is duplicated. Only additional logged-in
// users get their own platform instance.
//
// Per-session *stateful* modules (PlaybackManager/DirectStream/VideoCore/…) and the
// streaming split land in the following slices; for now non-admin sessions own the
// identity+collection read plane and delegate stateful playback to the shared
// (admin) modules.
type UserSession struct {
	app     *App
	UserID  uint
	IsAdmin bool

	// Non-admin only — admin reads these live off the App.
	user             *user.User
	anilistClientRef *util.Ref[anilist.AnilistClient]
	platformRef      *util.Ref[platform.Platform]
	// linked is true once this user has connected their own AniList account. An
	// unlinked non-admin user has an EMPTY collection (a clean slate) — it must never
	// fall back to the shared simulated/local collection, which is the admin's data.
	linked bool

	// Per-session stateful streaming/playback modules (non-admin only; admin reads
	// the App globals via the accessors). Built lazily/once on first access, each
	// wired to a ScopedWSEventManager fixed to this user so two users stream
	// independently. Shared engines (torrent client, debrid account, transcoder,
	// metadata, continuity) are injected by reference.
	modulesOnce    sync.Once
	continuityOnce sync.Once
	videoCore      *videocore.VideoCore
	mpvCore        *mpvcore.MpvCore
	nativePlayer   *nativeplayer.NativePlayer
	mediaCoord     *mediacore.Coordinator
	directStream   *directstream.Manager
	playback       *playbackmanager.PlaybackManager
	continuity     *continuity.Manager
}

// ensureContinuity lazily builds just this user's continuity (watch-history)
// manager — separate from the heavy streaming modules so reading resume data on an
// entry page doesn't spin up videoCore/directStream/etc.
func (s *UserSession) ensureContinuity() {
	s.continuityOnce.Do(func() {
		a := s.app
		s.continuity = continuity.NewManager(&continuity.NewManagerOptions{
			FileCacher: a.FileCacher,
			Logger:     a.Logger,
			Database:   a.Database,
			BucketName: fmt.Sprintf("%s_u%d", continuity.WatchHistoryBucketName, s.UserID),
		})
		if settings, err := a.Database.GetSettings(); err == nil && settings != nil && settings.Library != nil {
			eff := settings
			if ov, _ := a.Database.GetUserOverrides(s.UserID); ov != nil {
				eff = db.CloneSettings(settings)
				ov.ApplyTo(eff)
			}
			s.continuity.SetSettings(&continuity.Settings{
				WatchContinuityEnabled: eff.GetLibrary().EnableWatchContinuity,
			})
		}
	})
}

// ensureModules lazily builds this (non-admin) session's own streaming/playback
// modules. The admin never calls this (its accessors return the App globals).
func (s *UserSession) ensureModules() {
	s.modulesOnce.Do(func() {
		a := s.app
		scoped := events.NewScopedWSEventManager(a.WSEventManager, s.UserID)
		refresh := func() { _, _ = s.RefreshAnimeCollection() }
		// Child logger so EVERY log line from this user's session modules (videocore,
		// nativeplayer, directstream, playback) carries the username — per-user attribution
		// without tagging each call site.
		uname := "anon"
		if u, err := a.Database.GetUserByID(s.UserID); err == nil && u != nil {
			uname = u.Username
		}
		sl := a.Logger.With().Str("user", uname).Logger()
		sessionLogger := &sl

		// Per-user continuity (resume positions): own file-cache bucket so a user's
		// watch history never overwrites another's for the same media.
		s.ensureContinuity()

		s.videoCore = videocore.New(videocore.NewVideoCoreOptions{
			WsEventManager:             scoped,
			Logger:                     sessionLogger,
			ContinuityManager:          s.continuity,
			MetadataProviderRef:        a.MetadataProviderRef,
			DiscordPresence:            a.DiscordPresence,
			PlatformRef:                s.platformRef,
			RefreshAnimeCollectionFunc: refresh,
			IsOfflineRef:               a.IsOfflineRef(),
			// Per-session: process only this user's client (player) events.
			UserID:                s.UserID,
			AcceptUnscopedClients: false,
		})
		s.nativePlayer = nativeplayer.New(nativeplayer.NewNativePlayerOptions{
			WsEventManager: scoped,
			Logger:         sessionLogger,
			VideoCore:      s.videoCore,
		})
		s.mpvCore = mpvcore.New(mpvcore.NewMpvCoreOptions{
			WsEventManager:             scoped,
			Logger:                     sessionLogger,
			ContinuityManager:          s.continuity,
			MetadataProviderRef:        a.MetadataProviderRef,
			DiscordPresence:            a.DiscordPresence,
			PlatformRef:                s.platformRef,
			RefreshAnimeCollectionFunc: refresh,
			IsOfflineRef:               a.IsOfflineRef(),
			// Per-session: process only this user's client (player) events.
			UserID:                s.UserID,
			AcceptUnscopedClients: false,
		})
		// Per-session mediacore coordinator: since v3.9 ALL player signaling
		// (open/watch/abort/error) goes through the coordinator, so a session
		// without one can resolve a stream but never tell the client to open the
		// player. Mirrors the App-global wiring in modules.go.
		s.mediaCoord = mediacore.NewCoordinator(mediacore.NewCoordinatorOptions{
			Logger:                     sessionLogger,
			MetadataProviderRef:        a.MetadataProviderRef,
			ContinuityManager:          s.continuity,
			DiscordPresence:            a.DiscordPresence,
			PlatformRef:                s.platformRef,
			RefreshAnimeCollectionFunc: refresh,
			IsOfflineRef:               a.IsOfflineRef(),
			Backends: map[player.Target]mediacore.Backend{
				player.TargetVideoCore: videocore.NewAdapter(s.videoCore, s.nativePlayer),
				player.TargetMpvCore:   mpvcore.NewAdapter(s.mpvCore),
			},
		})
		s.mediaCoord.SetupSharedEffects()
		s.directStream = directstream.NewManager(directstream.NewManagerOptions{
			Logger:                     sessionLogger,
			WSEventManager:             scoped,
			ContinuityManager:          s.continuity,
			MetadataProviderRef:        a.MetadataProviderRef,
			DiscordPresence:            a.DiscordPresence,
			PlatformRef:                s.platformRef,
			RefreshAnimeCollectionFunc: refresh,
			IsOfflineRef:               a.IsOfflineRef(),
			NativePlayer:               s.nativePlayer,
			VideoCore:                  s.videoCore,
			MediacoreCoordinator:       s.mediaCoord,
			HMACTokenFunc: func(endpoint string, symbol string) string {
				qp, err := a.GetServerPasswordHMACAuth().GenerateQueryParam(endpoint, symbol)
				if err != nil {
					return ""
				}
				return qp
			},
		})
		s.playback = playbackmanager.New(&playbackmanager.NewPlaybackManagerOptions{
			Logger:                     sessionLogger,
			WSEventManager:             scoped,
			PlatformRef:                s.platformRef,
			MetadataProviderRef:        a.MetadataProviderRef,
			Database:                   a.Database,
			DiscordPresence:            a.DiscordPresence,
			IsOfflineRef:               a.IsOfflineRef(),
			ContinuityManager:          s.continuity,
			RefreshAnimeCollectionFunc: refresh,
			UserID:                     s.UserID,
		})

		// Apply the user's effective settings (shared server settings + their overrides).
		s.applyModuleSettings()
	})
}

// applyModuleSettings pushes the user's effective settings into their session
// modules (mirrors the relevant parts of App.InitOrRefreshModules).
func (s *UserSession) applyModuleSettings() {
	a := s.app
	settings, err := a.Database.GetSettings()
	if err != nil || settings == nil {
		return
	}
	if !s.IsAdmin {
		if ov, _ := a.Database.GetUserOverrides(s.UserID); ov != nil {
			settings = db.CloneSettings(settings)
			ov.ApplyTo(settings)
		}
	}
	if s.videoCore != nil {
		s.videoCore.SetSettings(settings)
	}
	if s.mpvCore != nil {
		s.mpvCore.SetSettings(settings)
	}
	if s.mediaCoord != nil {
		s.mediaCoord.SetSettings(settings)
	}
	if settings.Library != nil && s.playback != nil {
		if a.MediaPlayerRepository != nil {
			s.playback.SetMediaPlayerRepository(a.MediaPlayerRepository)
		}
		s.playback.SetSettings(&playbackmanager.Settings{
			AutoPlayNextEpisode: settings.GetLibrary().AutoPlayNextEpisode,
		})
	}
	if settings.Library != nil && s.directStream != nil {
		s.directStream.SetSettings(&directstream.Settings{
			AutoPlayNextEpisode: settings.GetLibrary().AutoPlayNextEpisode,
			AutoUpdateProgress:  settings.GetLibrary().AutoUpdateProgress,
		})
	}
	if s.directStream != nil {
		// Mirror modules.go: without this, session managers keep the constructor
		// default (videocore) and MpvCore clients never get their open signal.
		playbackTarget := directstream.PlaybackTargetVideoCore
		if settings.GetMediaPlayer().MpvPrismEnabled {
			playbackTarget = directstream.PlaybackTargetMpvCore
		}
		s.directStream.SetPlaybackTarget(playbackTarget)
	}
	// continuity settings are applied in ensureContinuity (its own lazy init).
}

// Continuity returns the session's continuity (watch-history) manager (admin → App global).
func (s *UserSession) Continuity() *continuity.Manager {
	if s.IsAdmin {
		return s.app.ContinuityManager
	}
	s.ensureContinuity()
	return s.continuity
}

// DirectStream returns the session's DirectStream manager (admin → App global).
func (s *UserSession) DirectStream() *directstream.Manager {
	if s.IsAdmin {
		return s.app.DirectStreamManager
	}
	s.ensureModules()
	return s.directStream
}

// Playback returns the session's PlaybackManager (admin → App global).
func (s *UserSession) Playback() *playbackmanager.PlaybackManager {
	if s.IsAdmin {
		return s.app.PlaybackManager
	}
	s.ensureModules()
	return s.playback
}

// VideoCore returns the session's VideoCore (admin → App global).
func (s *UserSession) VideoCore() *videocore.VideoCore {
	if s.IsAdmin {
		return s.app.VideoCore
	}
	s.ensureModules()
	return s.videoCore
}

// NativePlayer returns the session's NativePlayer (admin → App global).
func (s *UserSession) NativePlayer() *nativeplayer.NativePlayer {
	if s.IsAdmin {
		return s.app.NativePlayer
	}
	s.ensureModules()
	return s.nativePlayer
}

// Events returns the WS event manager scoped to this session's user, so a user's
// streaming overlay/loader events (debrid "Selecting/Adding torrent…", indefinite
// loaders) reach only them. Admin → the App's admin-scoped manager (the same instance
// the global modules use); non-admin → a manager fixed to their user id.
func (s *UserSession) Events() events.WSEventManagerInterface {
	if s.IsAdmin {
		return s.app.adminEvents
	}
	return events.NewScopedWSEventManager(s.app.WSEventManager, s.UserID)
}

func emptyAnimeCollection() *anilist.AnimeCollection {
	return &anilist.AnimeCollection{
		MediaListCollection: &anilist.AnimeCollection_MediaListCollection{
			Lists: []*anilist.AnimeCollection_MediaListCollection_Lists{},
		},
	}
}

func emptyMangaCollection() *anilist.MangaCollection {
	return &anilist.MangaCollection{
		MediaListCollection: &anilist.MangaCollection_MediaListCollection{
			Lists: []*anilist.MangaCollection_MediaListCollection_Lists{},
		},
	}
}

// ResolveDirectStreamManager finds the DirectStream manager whose active stream has
// the given playback id (the ?id= in a serve URL). With per-user sessions several
// managers may be streaming at once; the id disambiguates them. Falls back to the
// admin global when no match (back-compat / unknown id).
func (a *App) ResolveDirectStreamManager(id string) *directstream.Manager {
	if a.DirectStreamManager != nil && a.DirectStreamManager.ServesStreamID(id) {
		return a.DirectStreamManager
	}
	var found *directstream.Manager
	a.sessions.Range(func(_ uint, s *UserSession) bool {
		if s != nil && s.directStream != nil && s.directStream.ServesStreamID(id) {
			found = s.directStream
			return false
		}
		return true
	})
	if found != nil {
		return found
	}
	return a.DirectStreamManager
}

// ResolveDirectStreamManagerWithAttachment finds the DirectStream manager whose active
// stream contains the named attachment (font). Font subresource requests from the player
// carry no user session or ?id=, so they can't be resolved by user — but the font lives
// in exactly one active stream. Returns nil when no active stream has it.
func (a *App) ResolveDirectStreamManagerWithAttachment(filename string) *directstream.Manager {
	if a.DirectStreamManager != nil && a.DirectStreamManager.HasAttachment(filename) {
		return a.DirectStreamManager
	}
	var found *directstream.Manager
	a.sessions.Range(func(_ uint, s *UserSession) bool {
		if s != nil && s.directStream != nil && s.directStream.HasAttachment(filename) {
			found = s.directStream
			return false
		}
		return true
	})
	return found
}

// SessionFor resolves the session for a user id. The admin (or a zero id / unknown
// user) gets the App-global delegate; any other user gets a lazily-built, cached
// per-user session.
func (a *App) SessionFor(userID uint) *UserSession {
	if userID == 0 {
		// No resolved user. The admin delegate applies only for local (password-less)
		// installs, where the operator is the trusted admin. On a networked
		// (password-protected) server an unauthenticated request gets an anonymous,
		// data-less session: it must log in. This is what stops a client that only
		// knows the shared server password from inheriting admin data.
		if a.Config.Server.Password == "" {
			return a.adminSession()
		}
		return a.anonymousSession()
	}
	// Local/password-less install: the admin is the trusted single operator and uses the
	// App-global plane directly (zero overhead, single-user behaviour unchanged). On a
	// networked (password-protected) server EVERY user — admin included — gets their own
	// independent session (own AniList account, cache, modules, data) so multiple users,
	// even multiple admins, use the server independently.
	if a.Config.Server.Password == "" {
		if admin, err := a.Database.GetAdminUser(); err == nil && admin != nil && admin.ID == userID {
			return a.adminSession()
		}
	}
	sess, err := a.sessions.GetOrSet(userID, func() (*UserSession, error) {
		return a.buildUserSession(userID), nil
	})
	if err != nil || sess == nil {
		return a.adminSession()
	}
	return sess
}

// adminSession returns a fresh delegate over the App globals. It is intentionally
// not cached: the App's user/platform can be swapped (login/logout) and the
// accessors read them live, so a per-call struct is always current and cheap.
func (a *App) adminSession() *UserSession {
	return &UserSession{app: a, IsAdmin: true, UserID: a.adminUserID()}
}

// anonymousSession is the data-less session for an unauthenticated request on a
// networked server: empty collections (clean slate) + an unauthenticated AniList
// platform for public browse/search only. Built once and cached.
func (a *App) anonymousSession() *UserSession {
	a.anonSessionOnce.Do(func() {
		clientRef := util.NewRef[anilist.AnilistClient](anilist.NewAnilistClient("", userAnilistCacheDir(a.AnilistCacheDir, 0)))
		plat := anilist_platform.NewAnilistPlatform(clientRef, a.ExtensionBankRef, a.Logger, a.Database, func() {})
		a.anonSession = &UserSession{
			app:              a,
			UserID:           0,
			user:             user.NewSimulatedUser(),
			anilistClientRef: clientRef,
			platformRef:      util.NewRef[platform.Platform](plat),
			linked:           false,
		}
	})
	return a.anonSession
}

func (a *App) adminUserID() uint {
	if admin, err := a.Database.GetAdminUser(); err == nil && admin != nil {
		return admin.ID
	}
	return 0
}

// buildUserSession constructs a user's own AniList platform + per-user cache from their
// linked account token (or an unauthenticated/empty platform if they haven't linked one).
// On a networked server this runs for EVERY user including admins, so each gets an
// independent identity/cache/module plane. (On a local/password-less install the admin
// short-circuits to the App-global delegate in SessionFor and never reaches here.)
func (a *App) buildUserSession(userID uint) *UserSession {
	u, err := a.Database.GetUserByID(userID)
	if err != nil || u == nil {
		return a.adminSession()
	}

	token := ""
	var usr *user.User
	if acc, err := a.Database.GetAccountForUser(u); err == nil && acc != nil {
		token = acc.Token
		if built, err := user.NewUser(acc); err == nil {
			usr = built
		}
	}
	if usr == nil {
		usr = user.NewSimulatedUser()
	}

	clientRef := util.NewRef[anilist.AnilistClient](anilist.NewAnilistClient(token, userAnilistCacheDir(a.AnilistCacheDir, userID)))
	linked := clientRef.Get().IsAuthenticated()

	// Always build a real AniList platform — NOT the simulated platform, which is
	// backed by the shared LocalManager (the admin's offline data). An unlinked user
	// gets a clean slate: empty collections (see the getters below) + working public
	// browse/search through the unauthenticated client.
	plat := anilist_platform.NewAnilistPlatform(clientRef, a.ExtensionBankRef, a.Logger, a.Database, func() {
		a.logoutUserFromAnilist(userID)
	})
	if linked {
		plat.SetUsername(usr.Viewer.Name)
	}

	return &UserSession{
		app:              a,
		UserID:           userID,
		user:             usr,
		anilistClientRef: clientRef,
		platformRef:      util.NewRef[platform.Platform](plat),
		linked:           linked,
	}
}

// LoginUserToAnilist links an AniList token to a specific user, persisting their own
// Account row and (re)building their session. The admin routes through the existing
// App-global LoginToAnilist so the shared modules stay wired exactly as before.
func (a *App) LoginUserToAnilist(userID uint, token string) error {
	admin, _ := a.Database.GetAdminUser()
	if userID == 0 || (admin != nil && admin.ID == userID) {
		return a.LoginToAnilist(token)
	}

	if token == "" {
		return errInvalidToken
	}

	client := anilist.NewAnilistClient(token, a.AnilistCacheDir)
	getViewer, err := client.GetViewer(context.Background())
	if err != nil {
		a.Logger.Error().Err(err).Msg("app: User could not authenticate to AniList")
		return err
	}
	if getViewer == nil || getViewer.Viewer == nil || len(getViewer.Viewer.Name) == 0 {
		return errInvalidToken
	}

	viewerBytes, _ := json.Marshal(getViewer.Viewer)
	if _, err := a.Database.UpsertAccountForUser(userID, getViewer.Viewer.Name, token, viewerBytes); err != nil {
		return err
	}

	// Drop any cached session so the next resolve rebuilds with the new token.
	a.evictSession(userID)
	a.Logger.Info().Uint("userId", userID).Msg("app: User authenticated to AniList")
	return nil
}

// LogoutUserFromAnilist unlinks a user's AniList account. The admin routes through
// the App-global logout (switches the shared platform to simulated); other users
// just clear their own account and session.
func (a *App) LogoutUserFromAnilist(userID uint) {
	admin, _ := a.Database.GetAdminUser()
	if userID == 0 || (admin != nil && admin.ID == userID) {
		a.LogoutFromAnilist()
		return
	}
	a.logoutUserFromAnilist(userID)
}

// logoutUserFromAnilist clears a user's AniList token (called when their token is
// detected invalid) and evicts their session so it rebuilds as simulated.
func (a *App) logoutUserFromAnilist(userID uint) {
	if u, err := a.Database.GetUserByID(userID); err == nil && u != nil {
		_, _ = a.Database.UpsertAccountForUser(userID, "", "", nil)
	}
	a.evictSession(userID)
}

// evictSession tears down a cached user session's per-user streaming engines before removing
// it, so a relink/logout/token-invalid cycle doesn't leak their listener goroutines. Rebuilds
// happen lazily on the next resolve.
func (a *App) evictSession(userID uint) {
	if sess, ok := a.sessions.Get(userID); ok {
		sess.shutdown()
	}
	a.sessions.Delete(userID)
}

// shutdown stops this session's per-user streaming/playback engines (each spawns a listener
// goroutine bound to a subscriber channel that is never closed on eviction otherwise). Safe on
// an admin/empty session: modules are built lazily, so the nil checks skip unbuilt ones.
// directStream unsubscribes from mediaCoord, so tear it down before closing the coordinator.
func (s *UserSession) shutdown() {
	if s == nil {
		return
	}
	if s.directStream != nil {
		s.directStream.Shutdown()
	}
	if s.mediaCoord != nil {
		_ = s.mediaCoord.Close()
	}
	if s.mpvCore != nil {
		s.mpvCore.Shutdown()
	}
	if s.videoCore != nil {
		s.videoCore.Shutdown()
	}
}

// -------------------------------------------------------------------------------- //
// Identity + collection accessors
// -------------------------------------------------------------------------------- //

func (s *UserSession) PlatformRef() *util.Ref[platform.Platform] {
	if s.IsAdmin {
		return s.app.AnilistPlatformRef
	}
	return s.platformRef
}

func (s *UserSession) Platform() platform.Platform {
	return s.PlatformRef().Get()
}

func (s *UserSession) User() *user.User {
	if s.IsAdmin {
		return s.app.GetUser()
	}
	if s.user == nil {
		return user.NewSimulatedUser()
	}
	return s.user
}

func (s *UserSession) Username() string {
	u := s.User()
	if u == nil || u.Viewer == nil {
		return ""
	}
	return u.Viewer.GetName()
}

// AnilistToken returns the user's AniList token, or "" for a simulated user.
func (s *UserSession) AnilistToken() string {
	u := s.User()
	if u == nil || u.Token == user.SimulatedUserToken {
		return ""
	}
	return u.Token
}

func (s *UserSession) GetAnimeCollection(bypassCache bool) (*anilist.AnimeCollection, error) {
	if s.IsAdmin {
		return s.app.GetAnimeCollection(bypassCache)
	}
	if !s.linked {
		return emptyAnimeCollection(), nil
	}
	return s.platformRef.Get().GetAnimeCollection(context.Background(), bypassCache)
}

func (s *UserSession) GetRawAnimeCollection(bypassCache bool) (*anilist.AnimeCollection, error) {
	if s.IsAdmin {
		return s.app.GetRawAnimeCollection(bypassCache)
	}
	if !s.linked {
		return emptyAnimeCollection(), nil
	}
	return s.platformRef.Get().GetRawAnimeCollection(context.Background(), bypassCache)
}

func (s *UserSession) GetMangaCollection(bypassCache bool) (*anilist.MangaCollection, error) {
	if s.IsAdmin {
		return s.app.GetMangaCollection(bypassCache)
	}
	if !s.linked {
		return emptyMangaCollection(), nil
	}
	return s.platformRef.Get().GetMangaCollection(context.Background(), bypassCache)
}

func (s *UserSession) GetRawMangaCollection(bypassCache bool) (*anilist.MangaCollection, error) {
	if s.IsAdmin {
		return s.app.GetRawMangaCollection(bypassCache)
	}
	if !s.linked {
		return emptyMangaCollection(), nil
	}
	return s.platformRef.Get().GetRawMangaCollection(context.Background(), bypassCache)
}

// RefreshAnimeCollection refreshes the session's collection. For the admin this is
// the full App refresh (which fans the collection out to the shared stateful
// modules); for a non-admin it refreshes only their platform and notifies just that
// user (per-session stateful modules arrive in the next slice).
func (s *UserSession) RefreshAnimeCollection() (*anilist.AnimeCollection, error) {
	if s.IsAdmin {
		return s.app.RefreshAnimeCollection()
	}
	if !s.linked {
		return emptyAnimeCollection(), nil
	}
	ret, err := s.platformRef.Get().RefreshAnimeCollection(context.Background())
	if err != nil {
		return nil, err
	}
	// Fan the refreshed collection out to this user's per-session stateful modules. Without this
	// the session's directStream/playback never learn the collection (or serve a stale one after
	// the user adds media to their list) — the cause of "cannot play local file, anime collection
	// is not set". nil-guarded because modules are built lazily on first playback.
	if s.directStream != nil {
		s.directStream.SetAnimeCollection(ret)
	}
	if s.playback != nil {
		s.playback.SetAnimeCollection(ret)
	}
	s.app.WSEventManager.SendEventToUser(s.UserID, events.RefreshedAnilistAnimeCollection, nil)
	return ret, nil
}

func (s *UserSession) RefreshMangaCollection() (*anilist.MangaCollection, error) {
	if s.IsAdmin {
		return s.app.RefreshMangaCollection()
	}
	if !s.linked {
		return emptyMangaCollection(), nil
	}
	mc, err := s.platformRef.Get().RefreshMangaCollection(context.Background())
	if err != nil {
		return nil, err
	}
	s.app.WSEventManager.SendEventToUser(s.UserID, events.RefreshedAnilistMangaCollection, nil)
	return mc, nil
}
