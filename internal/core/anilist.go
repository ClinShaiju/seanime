package core

import (
	"context"
	"errors"
	"seanime/internal/api/anilist"
	"seanime/internal/database/models"
	"seanime/internal/events"
	"seanime/internal/platforms/anilist_platform"
	"seanime/internal/platforms/platform"
	"seanime/internal/platforms/shared_platform"
	"seanime/internal/platforms/simulated_platform"
	"seanime/internal/user"
	"seanime/internal/util"
	"time"

	"github.com/goccy/go-json"
)

// GetUser returns the currently logged-in user or a simulated one.
func (a *App) GetUser() *user.User {
	if a.user == nil {
		return user.NewSimulatedUser()
	}
	return a.user
}

// GetUsername returns the username of the currently logged-in user
func (a *App) GetUsername() string {
	if a.user == nil {
		return ""
	}
	if a.user.Viewer == nil {
		return ""
	}
	return a.user.Viewer.GetName()
}

func (a *App) GetUserAnilistToken() string {
	if a.user == nil || a.user.Token == user.SimulatedUserToken {
		return ""
	}

	return a.user.Token
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// UpdatePlatform changes the current platform to the provided one.
func (a *App) UpdatePlatform(platform platform.Platform) {
	if a.AnilistPlatformRef.IsPresent() {
		a.AnilistPlatformRef.Get().Close()
	}
	a.AnilistPlatformRef.Set(platform)
	a.AddOnRefreshAnilistCollectionFunc("anilist-platform", func() {
		a.AnilistPlatformRef.Get().ClearCache()
	})
}

// UpdateAnilistClientToken will update the Anilist Client Wrapper token.
// This function should be called when a user logs in
func (a *App) UpdateAnilistClientToken(token string) {
	ac := anilist.NewAnilistClient(token, a.AnilistCacheDir)
	a.AnilistClientRef.Set(ac)
}

func (a *App) UseOfficialAnilistClient() error {
	previousProvider := anilist.CurrentRequestProvider()
	anilist.UseOfficialAPI()

	if err := a.applyRuntimeAnilistClient(anilist.NewAnilistClient(a.Database.GetAnilistToken(), a.AnilistCacheDir)); err != nil {
		_ = anilist.SetRequestProvider(previousProvider)
		return err
	}

	return nil
}

func (a *App) UseCustomAnimeClient(config anilist.CustomClientConfig) error {
	previousProvider := anilist.CurrentRequestProvider()
	if err := anilist.UseCustomAPI(config); err != nil {
		return err
	}

	if err := a.applyRuntimeAnilistClient(anilist.NewAnilistClient(config.Token, a.AnilistCacheDir)); err != nil {
		_ = anilist.SetRequestProvider(previousProvider)
		return err
	}

	return nil
}

func (a *App) applyRuntimeAnilistClient(client anilist.AnilistClient) error {
	if a.IsOffline() {
		return errors.New("anilist runtime switch is unavailable in offline mode")
	}

	nextUser := user.NewSimulatedUser()
	provider := anilist.CurrentRequestProviderName()

	if client.IsAuthenticated() {
		viewer, err := fetchRuntimeViewer(provider, func() (*anilist.GetViewer, error) {
			return client.GetViewer(context.Background())
		}, time.Sleep)
		if err != nil {
			return err
		}

		token := ""
		if provider == anilist.OfficialRequestProviderName {
			token = a.Database.GetAnilistToken()
		}

		nextUser = &user.User{
			Viewer: viewer.GetViewer(),
			Token:  token,
		}
	}

	a.AnilistClientRef.Set(client)

	var nextPlatform platform.Platform
	var err error
	if client.IsAuthenticated() {
		nextPlatform = anilist_platform.NewAnilistPlatform(a.AnilistClientRef, a.ExtensionBankRef, a.Logger, a.Database, func() { a.autoLogoutFromAnilist(0) })
	} else {
		nextPlatform, err = simulated_platform.NewSimulatedPlatform(a.LocalManager, a.AnilistClientRef, a.ExtensionBankRef, a.Logger, a.Database)
		if err != nil {
			return err
		}
	}

	a.UpdatePlatform(nextPlatform)
	a.user = nextUser
	a.AnilistPlatformRef.Get().SetUsername(nextUser.Viewer.Name)
	a.InitOrRefreshModules()

	if a.DiscordPresence != nil {
		a.DiscordPresence.SetUsername(nextUser.Viewer.Name)
	}

	if _, err := a.RefreshAnimeCollection(); err != nil {
		return err
	}

	if _, err := a.RefreshMangaCollection(); err != nil {
		return err
	}

	return nil
}

var customClientViewerRetryDelays = []time.Duration{
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	3 * time.Second,
}

func fetchRuntimeViewer(provider string, getViewer func() (*anilist.GetViewer, error), sleep func(time.Duration)) (*anilist.GetViewer, error) {
	viewer, err := getViewer()
	if err == nil || provider == anilist.OfficialRequestProviderName {
		return viewer, err
	}

	for _, delay := range customClientViewerRetryDelays {
		sleep(delay)
		viewer, err = getViewer()
		if err == nil {
			return viewer, nil
		}
	}

	return nil, err
}

func (a *App) LoginToAnilist(token string) error {
	if token == "" {
		return errors.New("token is empty")
	}

	a.UpdateAnilistClientToken(token)

	getViewer, err := a.AnilistClientRef.Get().GetViewer(context.Background())
	if err != nil {
		a.Logger.Error().Msg("Could not authenticate to AniList")
		return err
	}

	if len(getViewer.Viewer.Name) == 0 {
		return errors.New("could not find user")
	}

	bytes, err := json.Marshal(getViewer.Viewer)
	if err != nil {
		a.Logger.Err(err).Msg("scan: could not save local files")
	}

	_, err = a.Database.UpsertAccount(&models.Account{
		BaseModel: models.BaseModel{
			ID:        1,
			UpdatedAt: time.Now(),
		},
		Username: getViewer.Viewer.Name,
		Token:    token,
		Viewer:   bytes,
	})
	if err != nil {
		return err
	}

	a.Logger.Info().Msg("app: Authenticated to AniList")

	// On a networked server the admin has its own cached per-user session (not the global
	// delegate); evict it so it rebuilds with the freshly-linked account/token.
	a.evictSession(a.adminUserID())

	anilistPlatform := anilist_platform.NewAnilistPlatform(a.AnilistClientRef, a.ExtensionBankRef, a.Logger, a.Database, func() { a.autoLogoutFromAnilist(0) })
	a.UpdatePlatform(anilistPlatform)

	a.InitOrRefreshAnilistData()
	a.InitOrRefreshModules()

	go func() {
		defer util.HandlePanicThen(func() {})
		a.InitOrRefreshTorrentstreamSettings()
		a.InitOrRefreshMediastreamSettings()
		a.InitOrRefreshDebridSettings()
	}()

	return nil
}

// LogoutFromAnilist clears the AniList token and switches to the simulated platform.
// This is called internally when the token is detected as invalid.
func (a *App) LogoutFromAnilist() {
	// prevent multiple concurrent calls (e.g. from parallel failing requests)
	if !a.logoutInProgress.CompareAndSwap(false, true) {
		return
	}
	defer a.logoutInProgress.Store(false)

	a.UpdateAnilistClientToken("")

	simulatedPlatform, err := simulated_platform.NewSimulatedPlatform(a.LocalManager, a.AnilistClientRef, a.ExtensionBankRef, a.Logger, a.Database)
	if err != nil {
		a.Logger.Error().Err(err).Msg("app: Failed to create simulated platform during auto-logout")
	} else {
		a.UpdatePlatform(simulatedPlatform)
	}

	_, _ = a.Database.UpsertAccount(&models.Account{
		BaseModel: models.BaseModel{
			ID:        1,
			UpdatedAt: time.Now(),
		},
		Username: "",
		Token:    "",
		Viewer:   nil,
	})

	a.Logger.Debug().Msg("app: Logged out from AniList, switched to simulated platform")

	// Networked server: evict the admin's cached per-user session so it rebuilds unlinked.
	a.evictSession(a.adminUserID())

	a.InitOrRefreshModules()
	a.InitOrRefreshAnilistData()
}

// autoLogoutFromAnilist is the logoutFunc handed to a platform's AniList cache layer. It
// runs when AniList answers a query with an auth error ("invalid token" / "user not
// found").
//
// Unlike the user-initiated logout it re-checks the token with a direct GetViewer call
// first. AniList returns the same "Invalid token" GraphQL error for transient/edge
// failures as it does for a genuinely revoked one, and this path DESTROYS the stored
// credential (UpsertAccount with an empty token) — so a single bad response permanently
// unlinks the account with no way back but a fresh OAuth round-trip. That is exactly what
// happened on 2026-08-12: one 400 on an AnimeCollection query unlinked the admin, leaving
// every profile showing the same blank simulated user.
//
// userID 0 means the App-global (admin) plane; any other id is that user's own account.
func (a *App) autoLogoutFromAnilist(userID uint) {
	// One failing page fires several queries, so this runs on several goroutines at once.
	// Serialize them: otherwise each verifies the token independently and the unluckiest
	// sample wins, which is how the credential died again on 2026-08-14 even though two
	// sibling checks passed seconds later.
	a.autoLogoutMu.Lock()
	defer a.autoLogoutMu.Unlock()

	token := ""
	if userID == 0 {
		token = a.Database.GetAnilistToken()
	} else if u, err := a.Database.GetUserByID(userID); err == nil && u != nil {
		if acc, err := a.Database.GetAccountForUser(u); err == nil && acc != nil {
			token = acc.Token
		}
	}

	if token == "" {
		// Nothing left to destroy: either an earlier caller in this same burst already
		// logged out, or the account was never linked.
		return
	}

	if a.anilistTokenStillAuthenticates(token) {
		a.Logger.Warn().Uint("userId", userID).
			Msg("app: Ignoring AniList auto-logout, the stored token still authenticates")
		return
	}

	// The cache layer no longer announces the expiry itself — it can't know whether the
	// logout will actually happen. Scope the notice to the affected user so one profile's
	// dead token doesn't tell everyone on the server that their session expired.
	const expiredMsg = "Your AniList session has expired. Please log in again."
	if userID == 0 {
		a.WSEventManager.SendEvent(events.ServerLoggedOutAnilist, expiredMsg)
	} else {
		events.NewScopedWSEventManager(a.WSEventManager, userID).SendEvent(events.ServerLoggedOutAnilist, expiredMsg)
	}

	if userID == 0 {
		a.LogoutFromAnilist()
		return
	}
	a.logoutUserFromAnilist(userID)
}

const (
	// A live token is answered with "Invalid token" often enough that one sample proves
	// nothing: on 2026-08-14 three collection queries got that 400 within a second while
	// the very same token authenticated twice a moment later. Only an unbroken run of
	// auth-shaped answers counts as a dead credential.
	anilistTokenChecks   = 3
	anilistTokenCheckGap = 3 * time.Second
)

// anilistTokenStillAuthenticates reports whether token is still accepted by AniList.
// A network/5xx failure counts as "still valid": refusing to destroy a credential while
// the API is unreachable is the safe direction to be wrong in.
func (a *App) anilistTokenStillAuthenticates(token string) bool {
	if token == "" {
		return false
	}
	client := anilist.NewAnilistClient(token, a.AnilistCacheDir)
	for i := 0; i < anilistTokenChecks; i++ {
		if i > 0 {
			time.Sleep(anilistTokenCheckGap)
		}
		if anilistCheckSaysAlive(client.GetViewer(context.Background())) {
			return true
		}
	}
	a.Logger.Warn().Int("checks", anilistTokenChecks).
		Msg("app: AniList rejected the stored token on every re-check, treating it as dead")
	return false
}

// anilistCheckSaysAlive reports whether one GetViewer answer clears the token. Only an
// auth-shaped error is a vote for "dead" — a network/5xx answer, or any other failure,
// means the API is unhappy for reasons that have nothing to do with our credential.
func anilistCheckSaysAlive(viewer *anilist.GetViewer, err error) bool {
	if err != nil {
		return !shared_platform.IsAnilistAuthError(err)
	}
	return viewer != nil && viewer.Viewer != nil && len(viewer.Viewer.Name) > 0
}

// GetAnimeCollection returns the user's Anilist collection if it in the cache, otherwise it queries Anilist for the user's collection.
// When bypassCache is true, it will always query Anilist for the user's collection
func (a *App) GetAnimeCollection(bypassCache bool) (*anilist.AnimeCollection, error) {
	return a.AnilistPlatformRef.Get().GetAnimeCollection(context.Background(), bypassCache)
}

// GetRawAnimeCollection is the same as GetAnimeCollection but returns the raw collection that includes custom lists
func (a *App) GetRawAnimeCollection(bypassCache bool) (*anilist.AnimeCollection, error) {
	return a.AnilistPlatformRef.Get().GetRawAnimeCollection(context.Background(), bypassCache)
}

func (a *App) SyncAnilistToSimulatedCollection() {
	if a.LocalManager != nil &&
		!a.GetUser().IsSimulated &&
		a.Settings != nil &&
		a.Settings.Library != nil &&
		a.Settings.Library.AutoSyncToLocalAccount {
		_ = a.LocalManager.SynchronizeAnilistToSimulatedCollection()
	}
}

// RefreshAnimeCollection queries Anilist for the user's collection
func (a *App) RefreshAnimeCollection() (*anilist.AnimeCollection, error) {
	go func() {
		a.OnRefreshAnilistCollectionFuncs.Range(func(key string, f func()) bool {
			go f()
			return true
		})
	}()

	ret, err := a.AnilistPlatformRef.Get().RefreshAnimeCollection(context.Background())

	if err != nil {
		return nil, err
	}

	// Save the collection to PlaybackManager
	a.PlaybackManager.SetAnimeCollection(ret)

	// Save the collection to AutoDownloader
	a.AutoDownloader.SetAnimeCollection(ret)

	// Save the collection to LocalManager
	a.LocalManager.SetAnimeCollection(ret)

	// Save the collection to DirectStreamManager
	a.DirectStreamManager.SetAnimeCollection(ret)

	// Save the collection to LibraryExplorer
	a.LibraryExplorer.SetAnimeCollection(ret)

	a.AutoScanner.SetAnimeCollection(ret)

	//a.SyncAnilistToSimulatedCollection()

	a.WSEventManager.SendEvent(events.RefreshedAnilistAnimeCollection, nil)

	return ret, nil
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// GetMangaCollection is the same as GetAnimeCollection but for manga
func (a *App) GetMangaCollection(bypassCache bool) (*anilist.MangaCollection, error) {
	return a.AnilistPlatformRef.Get().GetMangaCollection(context.Background(), bypassCache)
}

// GetRawMangaCollection does not exclude custom lists
func (a *App) GetRawMangaCollection(bypassCache bool) (*anilist.MangaCollection, error) {
	return a.AnilistPlatformRef.Get().GetRawMangaCollection(context.Background(), bypassCache)
}

// RefreshMangaCollection queries Anilist for the user's manga collection
func (a *App) RefreshMangaCollection() (*anilist.MangaCollection, error) {
	mc, err := a.AnilistPlatformRef.Get().RefreshMangaCollection(context.Background())

	if err != nil {
		return nil, err
	}

	a.LocalManager.SetMangaCollection(mc)

	a.WSEventManager.SendEvent(events.RefreshedAnilistMangaCollection, nil)

	return mc, nil
}
