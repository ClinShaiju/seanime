package core

import (
	"seanime/internal/api/anilist"
	"seanime/internal/continuity"
	"seanime/internal/database/db"
	"seanime/internal/database/db_bridge"
	"seanime/internal/database/models"
	debrid_client "seanime/internal/debrid/client"
	"seanime/internal/directstream"
	discordrpc_presence "seanime/internal/discordrpc/presence"
	"seanime/internal/events"
	"seanime/internal/library/anime"
	"seanime/internal/library/autodownloader"
	"seanime/internal/library/autoscanner"
	"seanime/internal/library/fillermanager"
	"seanime/internal/library/playbackmanager"
	"seanime/internal/library_explorer"
	"seanime/internal/manga"
	"seanime/internal/mediacore"
	"seanime/internal/mediaplayers/iina"
	"seanime/internal/mediaplayers/mediaplayer"
	"seanime/internal/mediaplayers/mpchc"
	"seanime/internal/mediaplayers/mpv"
	"seanime/internal/mediaplayers/vlc"
	"seanime/internal/mediastream"
	"seanime/internal/mpvcore"
	"seanime/internal/nakama"
	"seanime/internal/nativeplayer"
	"seanime/internal/notifier"
	"seanime/internal/platforms/shared_platform"
	"seanime/internal/player"
	"seanime/internal/playlist"
	"seanime/internal/plugin"
	seanime_torrent "seanime/internal/torrent_clients/builtin_client"
	"seanime/internal/torrent_clients/qbittorrent"
	"seanime/internal/torrent_clients/torrent_client"
	"seanime/internal/torrent_clients/transmission"
	"seanime/internal/torrents/torrent"
	"seanime/internal/torrentstream"
	"seanime/internal/user"
	"seanime/internal/util"
	"seanime/internal/videocore"

	"github.com/cli/browser"
	"github.com/rs/zerolog"
)

// systemUserID is a reserved user id used to scope the App-global (system) streaming
// modules on a networked server so they never match a real user's connection. No real
// user row can have this id.
const systemUserID = ^uint(0)

// initModulesOnce will initialize modules that need to persist.
// This function is called once after the App instance is created.
// The settings of these modules will be set/refreshed in InitOrRefreshModules.
func (a *App) initModulesOnce() {

	_, _ = util.InitIOSDocumentsDir()

	// Event manager for the App-global (admin's) streaming/playback modules: scoped to
	// the admin user so their events don't leak to other users. The modules below are
	// constructed with this instead of the raw WSEventManager. Non-admin users get
	// their own session modules scoped to themselves (session.go).
	adminID := uint(0)
	if admin, err := a.Database.GetAdminUser(); err == nil && admin != nil {
		adminID = admin.ID
	}
	// On a networked (password) server the admin has their OWN per-user session plane like
	// every user (buildUserSession leaves IsAdmin=false, so SessionFor returns per-user
	// modules scoped to the admin's id). If the global plane ALSO emitted to the admin's real
	// id, the admin's client would receive duplicate/contending stream events from both planes
	// and playback would never start — while regular users, whose id the global plane never
	// claims, work fine. So scope the global event plane to the system sentinel that matches no
	// real connection (same reasoning as the global VideoCore below). Local/password-less
	// installs keep using the admin id: there the admin IS the global plane (IsAdmin sessions
	// return the global modules) and there is no per-user duplicate.
	adminEventsUserID := adminID
	if a.Config.Server.Password != "" {
		adminEventsUserID = systemUserID
	}
	a.adminEvents = events.NewScopedWSEventManager(a.WSEventManager, adminEventsUserID)

	// Global VideoCore scope. On a local/password-less install the admin uses the global
	// modules directly, so the global VideoCore claims the admin's id and also accepts
	// untagged (local/desktop) connections. On a networked (password) server EVERY user —
	// including the admin — has their own session VideoCore, so the global one must NOT
	// claim any real user's client events (it would double-process the admin's). Scope it
	// to a system sentinel that matches no real connection.
	globalVideoCoreUserID := adminID
	globalAcceptUnscoped := true
	if a.Config.Server.Password != "" {
		globalVideoCoreUserID = systemUserID
		globalAcceptUnscoped = false
	}

	a.LocalManager.SetRefreshAnilistCollectionsFunc(func() {
		_, _ = a.RefreshAnimeCollection()
		_, _ = a.RefreshMangaCollection()
	})

	plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
		OnRefreshAnilistAnimeCollection: func() {
			_, _ = a.RefreshAnimeCollection()
		},
		OnRefreshAnilistMangaCollection: func() {
			_, _ = a.RefreshMangaCollection()
		},
	})

	// +---------------------+
	// |     Discord RPC     |
	// +---------------------+

	a.DiscordPresence = discordrpc_presence.New(nil, a.Logger)
	a.AddCleanupFunction(func() {
		a.DiscordPresence.Close()
	})

	plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
		DiscordPresence: a.DiscordPresence,
	})

	// +---------------------+
	// |       Filler        |
	// +---------------------+

	a.FillerManager = fillermanager.New(&fillermanager.NewFillerManagerOptions{
		DB:     a.Database,
		Logger: a.Logger,
	})

	plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
		FillerManager: a.FillerManager,
	})

	// +---------------------+
	// |     Continuity      |
	// +---------------------+

	a.ContinuityManager = continuity.NewManager(&continuity.NewManagerOptions{
		FileCacher: a.FileCacher,
		Logger:     a.Logger,
		Database:   a.Database,
	})

	// +---------------------+
	// |   Playback Manager  |
	// +---------------------+

	// Playback Manager
	a.PlaybackManager = playbackmanager.New(&playbackmanager.NewPlaybackManagerOptions{
		Logger:              a.Logger,
		WSEventManager:      a.adminEvents,
		PlatformRef:         a.AnilistPlatformRef,
		MetadataProviderRef: a.MetadataProviderRef,
		Database:            a.Database,
		DiscordPresence:     a.DiscordPresence,
		IsOfflineRef:        a.IsOfflineRef(),
		ContinuityManager:   a.ContinuityManager,
		RefreshAnimeCollectionFunc: func() {
			_, _ = a.RefreshAnimeCollection()
		},
	})

	// +---------------------+
	// | Torrent Repository  |
	// +---------------------+

	a.TorrentRepository = torrent.NewRepository(&torrent.NewRepositoryOptions{
		Logger:              a.Logger,
		MetadataProviderRef: a.MetadataProviderRef,
		ExtensionBankRef:    a.ExtensionBankRef,
	})

	a.AddCleanupFunction(func() {
		if a.TorrentClientRepository != nil {
			a.TorrentClientRepository.Shutdown()
		}
	})

	// +---------------------+
	// |  Manga Downloader   |
	// +---------------------+

	a.MangaDownloader = manga.NewDownloader(&manga.NewDownloaderOptions{
		Database:       a.Database,
		Logger:         a.Logger,
		WSEventManager: a.WSEventManager,
		DownloadDir:    a.Config.Manga.DownloadDir,
		Repository:     a.MangaRepository,
		IsOfflineRef:   a.IsOfflineRef(),
	})

	a.MangaDownloader.Start()

	// +---------------------+
	// |      VideoCore      |
	// +---------------------+

	a.VideoCore = videocore.New(videocore.NewVideoCoreOptions{
		WsEventManager:      a.adminEvents,
		Logger:              a.Logger,
		ContinuityManager:   a.ContinuityManager,
		MetadataProviderRef: a.MetadataProviderRef,
		DiscordPresence:     a.DiscordPresence,
		PlatformRef:         a.AnilistPlatformRef,
		RefreshAnimeCollectionFunc: func() {
			_, _ = a.RefreshAnimeCollection()
		},
		IsOfflineRef: a.IsOfflineRef(),
		// Local install: admin id + accept untagged conns. Networked: system sentinel so
		// the global VideoCore never claims a real user's client events (each user, incl
		// admin, has their own session VideoCore).
		UserID:                globalVideoCoreUserID,
		AcceptUnscopedClients: globalAcceptUnscoped,
	})

	// +---------------------+
	// |       MpvCore       |
	// +---------------------+

	a.MpvCore = mpvcore.New(mpvcore.NewMpvCoreOptions{
		WsEventManager:      a.adminEvents,
		Logger:              a.Logger,
		ContinuityManager:   a.ContinuityManager,
		MetadataProviderRef: a.MetadataProviderRef,
		DiscordPresence:     a.DiscordPresence,
		PlatformRef:         a.AnilistPlatformRef,
		RefreshAnimeCollectionFunc: func() {
			_, _ = a.RefreshAnimeCollection()
		},
		IsOfflineRef: a.IsOfflineRef(),
		// Same scoping rationale as the global VideoCore above: on a networked server
		// every user (incl. admin) has their own session MpvCore, so the global one must
		// not claim any real user's client events.
		UserID:                globalVideoCoreUserID,
		AcceptUnscopedClients: globalAcceptUnscoped,
	})

	// +---------------------+
	// |    Native Player    |
	// +---------------------+

	a.NativePlayer = nativeplayer.New(nativeplayer.NewNativePlayerOptions{
		WsEventManager: a.adminEvents,
		Logger:         a.Logger,
		VideoCore:      a.VideoCore,
	})

	// +-----------------------+
	// | Mediacore Coordinator |
	// +-----------------------+

	vcAdapter := videocore.NewAdapter(a.VideoCore, a.NativePlayer)
	mcAdapter := mpvcore.NewAdapter(a.MpvCore)

	a.MediacoreCoordinator = mediacore.NewCoordinator(mediacore.NewCoordinatorOptions{
		Logger:              a.Logger,
		MetadataProviderRef: a.MetadataProviderRef,
		ContinuityManager:   a.ContinuityManager,
		DiscordPresence:     a.DiscordPresence,
		PlatformRef:         a.AnilistPlatformRef,
		RefreshAnimeCollectionFunc: func() {
			_, _ = a.RefreshAnimeCollection()
		},
		IsOfflineRef: a.IsOfflineRef(),
		Backends: map[player.Target]mediacore.Backend{
			player.TargetVideoCore: vcAdapter,
			player.TargetMpvCore:   mcAdapter,
		},
	})

	a.AddCleanupFunction(func() {
		_ = a.MediacoreCoordinator.Close()
	})

	a.MediacoreCoordinator.SetupSharedEffects()

	// +---------------------+
	// |    Media Stream     |
	// +---------------------+

	a.MediastreamRepository = mediastream.NewRepository(&mediastream.NewRepositoryOptions{
		Logger:               a.Logger,
		WSEventManager:       a.adminEvents,
		FileCacher:           a.FileCacher,
		MediacoreCoordinator: a.MediacoreCoordinator,
	})

	a.AddCleanupFunction(func() {
		a.MediastreamRepository.OnCleanup()
	})

	// +---------------------+
	// |   Direct Stream     |
	// +---------------------+

	a.DirectStreamManager = directstream.NewManager(directstream.NewManagerOptions{
		Logger:              a.Logger,
		WSEventManager:      a.adminEvents,
		ContinuityManager:   a.ContinuityManager,
		MetadataProviderRef: a.MetadataProviderRef,
		DiscordPresence:     a.DiscordPresence,
		PlatformRef:         a.AnilistPlatformRef,
		RefreshAnimeCollectionFunc: func() {
			_, _ = a.RefreshAnimeCollection()
		},
		IsOfflineRef:         a.IsOfflineRef(),
		NativePlayer:         a.NativePlayer,
		VideoCore:            a.VideoCore,
		MediacoreCoordinator: a.MediacoreCoordinator,
		HMACTokenFunc: func(endpoint string, symbol string) string {
			qp, err := a.GetServerPasswordHMACAuth().GenerateQueryParam(endpoint, symbol)
			if err != nil {
				return ""
			}
			return qp
		},
	})

	// +---------------------+
	// |   Torrent Stream    |
	// +---------------------+

	a.TorrentstreamRepository = torrentstream.NewRepository(&torrentstream.NewRepositoryOptions{
		Logger:               a.Logger,
		BaseAnimeCache:       anilist.NewBaseAnimeCache(),
		CompleteAnimeCache:   anilist.NewCompleteAnimeCache(),
		MetadataProviderRef:  a.MetadataProviderRef,
		TorrentRepository:    a.TorrentRepository,
		PlatformRef:          a.AnilistPlatformRef,
		PlaybackManager:      a.PlaybackManager,
		WSEventManager:       a.adminEvents,
		Database:             a.Database,
		DirectStreamManager:  a.DirectStreamManager,
		MediacoreCoordinator: a.MediacoreCoordinator,
	})

	// +---------------------+
	// | Debrid Client Repo  |
	// +---------------------+

	a.DebridClientRepository = debrid_client.NewRepository(&debrid_client.NewRepositoryOptions{
		Logger:              a.Logger,
		WSEventManager:      a.adminEvents,
		Database:            a.Database,
		MetadataProviderRef: a.MetadataProviderRef,
		PlatformRef:         a.AnilistPlatformRef,
		PlaybackManager:     a.PlaybackManager,
		TorrentRepository:   a.TorrentRepository,
		DirectStreamManager: a.DirectStreamManager,
		// Route playback to the streaming user's own session modules (per-user streams).
		SessionModulesFunc: func(userID uint) (*directstream.Manager, *playbackmanager.PlaybackManager) {
			sess := a.SessionFor(userID)
			return sess.DirectStream(), sess.Playback()
		},
		// Route stream overlay/loader events to the streaming user (not always the admin).
		SessionEventsFunc: func(userID uint) events.WSEventManagerInterface {
			return a.SessionFor(userID).Events()
		},
	})

	plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
		PlaybackManager:      a.PlaybackManager,
		MangaRepository:      a.MangaRepository,
		VideoCore:            a.VideoCore,
		MediacoreCoordinator: a.MediacoreCoordinator,
		DirectStreamManager:  a.DirectStreamManager,
	})

	// +---------------------+
	// |   Auto Downloader   |
	// +---------------------+

	a.AutoDownloader = autodownloader.New(&autodownloader.NewAutoDownloaderOptions{
		Logger:                  a.Logger,
		TorrentClientRepository: a.TorrentClientRepository,
		TorrentRepository:       a.TorrentRepository,
		Database:                a.Database,
		WSEventManager:          a.WSEventManager,
		MetadataProviderRef:     a.MetadataProviderRef,
		DebridClientRepository:  a.DebridClientRepository,
		IsOfflineRef:            a.IsOfflineRef(),
	})

	// This is run in a goroutine
	a.AutoDownloader.Start()

	// +---------------------+
	// |    Auto Scanner     |
	// +---------------------+

	a.AutoScanner = autoscanner.New(&autoscanner.NewAutoScannerOptions{
		Database:            a.Database,
		PlatformRef:         a.AnilistPlatformRef,
		Logger:              a.Logger,
		WSEventManager:      a.WSEventManager,
		Enabled:             false, // Will be set in InitOrRefreshModules
		AutoDownloader:      a.AutoDownloader,
		MetadataProviderRef: a.MetadataProviderRef,
		LogsDir:             a.Config.Logs.Dir,
		OnRefreshCollection: func() {
			go func() {
				_, _ = a.RefreshAnimeCollection()
			}()
		},
	})

	// This is run in a goroutine
	a.AutoScanner.Start()

	// +---------------------+
	// |       Nakama        |
	// +---------------------+

	a.NakamaManager = nakama.NewManager(&nakama.NewManagerOptions{
		Logger:                  a.Logger,
		WSEventManager:          a.WSEventManager,
		PlaybackManager:         a.PlaybackManager,
		TorrentstreamRepository: a.TorrentstreamRepository,
		DebridClientRepository:  a.DebridClientRepository,
		PlatformRef:             a.AnilistPlatformRef,
		ServerHost:              a.Config.Server.Host,
		ServerPort:              a.Config.Server.Port,
		MediacoreCoordinator:    a.MediacoreCoordinator,
		DirectStreamManager:     a.DirectStreamManager,
		IsOfflineRef:            a.IsOfflineRef(),
	})

	// +---------------------+
	// |      Playlist       |
	// +---------------------+

	a.PlaylistManager = playlist.NewManager(&playlist.NewManagerOptions{
		TorrentstreamRepository: a.TorrentstreamRepository,
		DebridClientRepository:  a.DebridClientRepository,
		DirectStreamManager:     a.DirectStreamManager,
		PlatformRef:             a.AnilistPlatformRef,
		PlaybackManager:         a.PlaybackManager,
		WSEventManager:          a.WSEventManager,
		NakamaManager:           a.NakamaManager,
		MediacoreCoordinator:    a.MediacoreCoordinator,
		Database:                a.Database,
		Logger:                  a.Logger,
	})

	// +---------------------+
	// |   Anime Library     |
	// +---------------------+
	a.LibraryExplorer = library_explorer.NewLibraryExplorer(library_explorer.NewLibraryExplorerOptions{
		PlatformRef: a.AnilistPlatformRef,
		Logger:      a.Logger,
		Database:    a.Database,
	})

	// Background prewarm of the next-up episode of the last few watched shows (debrid only).
	// No-op until debrid + PreloadNextStream are configured.
	a.startContinueWatchingPrewarmLoop()

}

// HandleNewDatabaseEntries initializes essential database collections.
// It creates an empty local files collection if one does not already exist.
func HandleNewDatabaseEntries(database *db.Database, flags SeanimeFlags, logger *zerolog.Logger) {

	// Create initial empty local files collection if none exists
	if _, _, err := db_bridge.GetLocalFiles(database); err != nil {
		_, err := db_bridge.InsertLocalFiles(database, make([]*anime.LocalFile, 0))
		if err != nil {
			logger.Fatal().Err(err).Msgf("app: Failed to initialize local files in the database")
		}
	}

	bootstrapAdminUser(database, flags, logger)
}

// bootstrapAdminUser ensures the admin user (server owner) exists and, when invoked
// with --admin-username/--admin-password, creates or updates that admin credential
// (also the recovery path for a forgotten admin password). On a fresh install with
// no admin flags, it auto-generates a one-time password and logs it so the operator
// can perform the first login. The pre-existing AniList account (single-user data)
// is linked to the admin so their library/collection carries over.
func bootstrapAdminUser(database *db.Database, flags SeanimeFlags, logger *zerolog.Logger) {
	linkAccount := func(adminID uint) {
		if acc, err := database.GetAccount(); err == nil && acc != nil {
			_ = database.LinkAnilistAccount(adminID, acc.ID)
		}
	}

	existing, _ := database.GetAdminUser()

	switch {
	case flags.AdminPassword != "":
		// Explicit credential from flags: create or reset the admin.
		admin, err := database.SetAdminCredential(flags.AdminUsername, flags.AdminPassword)
		if err != nil {
			logger.Error().Err(err).Msg("app: Failed to set admin credential")
			return
		}
		linkAccount(admin.ID)
		logger.Warn().Str("username", admin.Username).Msg("app: Admin credential set from flags")

	case existing == nil:
		// Fresh install, no credential provided: generate a one-time password.
		username := flags.AdminUsername
		if username == "" {
			username = "admin"
		}
		password := util.GenerateCryptoID()
		admin, err := database.CreateUser(username, password, models.UserRoleAdmin)
		if err != nil {
			logger.Error().Err(err).Msg("app: Failed to bootstrap admin user")
			return
		}
		linkAccount(admin.ID)
		logger.Warn().Msgf("app: Created initial admin user %q with password: %s — log in and change it (set a new one with --admin-username/--admin-password)", username, password)

	default:
		// Admin already exists and no new credential was provided: nothing to do.
	}

	// Backfill legacy single-tenant per-user rows (user_id = 0) to the admin so a
	// single-user upgrade keeps its theme/data. Idempotent: only unassigned rows match.
	if admin, err := database.GetAdminUser(); err == nil && admin != nil {
		_ = database.Gorm().Model(&models.Theme{}).Where("user_id = ? OR user_id IS NULL", 0).Update("user_id", admin.ID).Error
		_ = database.Gorm().Model(&models.Playlist{}).Where("user_id = ? OR user_id IS NULL", 0).Update("user_id", admin.ID).Error
		_ = database.Gorm().Model(&models.AutoSelectProfile{}).Where("user_id = ? OR user_id IS NULL", 0).Update("user_id", admin.ID).Error
	}
}

// InitOrRefreshModules will initialize or refresh modules that depend on settings.
// This function is called:
//   - After the App instance is created
//   - After settings are updated.
//
// DEVNOTE: Make sure there's no blocking code in this function.
func (a *App) InitOrRefreshModules() {
	a.moduleMu.Lock()
	defer a.moduleMu.Unlock()

	a.Logger.Debug().Msgf("app: Refreshing modules")

	// Stop watching if already watching
	if a.Watcher != nil {
		a.Watcher.StopWatching()
	}

	// If Discord presence is already initialized, close it
	if a.DiscordPresence != nil {
		a.DiscordPresence.Close()
	}

	// Get settings from database
	settings, err := a.Database.GetSettings()
	if err != nil || settings == nil {
		a.Logger.Warn().Msg("app: Did not initialize modules, no settings found")
		return
	}

	a.Settings = settings // Store settings instance in app
	if settings.Library != nil {
		a.LibraryDir = settings.GetLibrary().LibraryPath

		if a.MetadataProviderRef.IsPresent() {
			a.MetadataProviderRef.Get().SetUseFallbackProvider(settings.GetLibrary().UseFallbackMetadataProvider)
		}
	}

	if settings.Anilist != nil {
		shared_platform.ShouldCache.Store(!settings.Anilist.DisableCacheLayer)
	}

	// +---------------------+
	// |   Module settings   |
	// +---------------------+
	// Refresh settings of modules that were initialized in initModulesOnce

	notifier.GlobalNotifier.SetSettings(a.Config.Data.AppDataDir, a.Settings.GetNotifications(), a.Logger)

	// Refresh updater settings
	if settings.Library != nil {
		plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
			AnimeLibraryPaths: a.Database.AllLibraryPathsFromSettings(settings),
		})

		if a.Updater != nil {
			a.Updater.SetEnabled(!settings.Library.DisableUpdateCheck)
			if settings.Library.UpdateChannel != "" {
				a.Updater.UpdateChannel = settings.Library.UpdateChannel
			} else {
				a.Updater.UpdateChannel = "github"
			}
		}

		// Refresh auto scanner settings (thread safe)
		if a.AutoScanner != nil {
			go a.AutoScanner.SetSettings(*settings.Library)
		}

		// Update the torrent manager settings (thread safe)
		go a.TorrentRepository.SetSettings(&torrent.RepositorySettings{
			DefaultAnimeProvider: settings.Library.TorrentProvider,
			AutoSelectProvider:   settings.Library.AutoSelectTorrentProvider,
		})

		if a.LibraryExplorer != nil {
			// Update the library paths for the library explorer (thread safe)
			go a.LibraryExplorer.SetLibraryPaths(settings.GetLibrary().GetLibraryPaths())
		}
	}

	if settings.MediaPlayer != nil {
		a.MediaPlayer.VLC = &vlc.VLC{
			Host:     settings.MediaPlayer.Host,
			Port:     settings.MediaPlayer.VlcPort,
			Password: settings.MediaPlayer.VlcPassword,
			Path:     settings.MediaPlayer.VlcPath,
			Logger:   a.Logger,
		}
		a.MediaPlayer.MpcHc = &mpchc.MpcHc{
			Host:   settings.MediaPlayer.Host,
			Port:   settings.MediaPlayer.MpcPort,
			Path:   settings.MediaPlayer.MpcPath,
			Logger: a.Logger,
		}
		a.MediaPlayer.Mpv = mpv.New(a.Logger, settings.MediaPlayer.MpvSocket, settings.MediaPlayer.MpvPath, settings.MediaPlayer.MpvArgs)
		a.MediaPlayer.Iina = iina.New(a.Logger, settings.MediaPlayer.IinaSocket, settings.MediaPlayer.IinaPath, settings.MediaPlayer.IinaArgs)

		// Set media player repository
		a.MediaPlayerRepository = mediaplayer.NewRepository(&mediaplayer.NewRepositoryOptions{
			Logger:            a.Logger,
			Default:           settings.MediaPlayer.Default,
			VLC:               a.MediaPlayer.VLC,
			MpcHc:             a.MediaPlayer.MpcHc,
			Mpv:               a.MediaPlayer.Mpv, // Socket
			Iina:              a.MediaPlayer.Iina,
			WSEventManager:    a.adminEvents,
			ContinuityManager: a.ContinuityManager,
		})

		a.PlaybackManager.SetMediaPlayerRepository(a.MediaPlayerRepository)
		a.PlaybackManager.SetSettings(&playbackmanager.Settings{
			AutoPlayNextEpisode: a.Settings.GetLibrary().AutoPlayNextEpisode,
		})

		a.DirectStreamManager.SetSettings(&directstream.Settings{
			AutoPlayNextEpisode: a.Settings.GetLibrary().AutoPlayNextEpisode,
			AutoUpdateProgress:  a.Settings.GetLibrary().AutoUpdateProgress,
		})

		playbackTarget := directstream.PlaybackTargetVideoCore
		if a.Settings.GetMediaPlayer().MpvPrismEnabled {
			playbackTarget = directstream.PlaybackTargetMpvCore
		}
		a.DirectStreamManager.SetPlaybackTarget(playbackTarget)

		a.TorrentstreamRepository.SetMediaPlayerRepository(a.MediaPlayerRepository)

		plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
			MediaPlayerRepository: a.MediaPlayerRepository,
		})
	} else {
		a.Logger.Warn().Msg("app: Did not initialize media player module, no settings found")
	}

	if a.VideoCore != nil {
		a.VideoCore.SetSettings(settings)
	}
	if a.MpvCore != nil {
		a.MpvCore.SetSettings(settings)
	}
	if a.MediacoreCoordinator != nil {
		a.MediacoreCoordinator.SetSettings(settings)
	}

	// Re-apply effective settings to live per-user sessions — they only self-apply
	// once at build, so changes like toggling MpvPrism (playback target) would
	// otherwise never reach an already-built session's modules.
	a.sessions.Range(func(_ uint, s *UserSession) bool {
		if s != nil {
			s.applyModuleSettings()
		}
		return true
	})

	// +---------------------+
	// |       Torrents      |
	// +---------------------+

	if settings.Torrent != nil {
		// Init qBittorrent
		qbit := qbittorrent.NewClient(&qbittorrent.NewClientOptions{
			Logger:   a.Logger,
			Username: settings.Torrent.QBittorrentUsername,
			Password: settings.Torrent.QBittorrentPassword,
			Port:     settings.Torrent.QBittorrentPort,
			Host:     settings.Torrent.QBittorrentHost,
			Path:     settings.Torrent.QBittorrentPath,
			Tags:     settings.Torrent.QBittorrentTags,
			Category: settings.Torrent.QBittorrentCategory,
		})
		// Login to qBittorrent
		go func() {
			if settings.Torrent.Default == "qbittorrent" {
				err = qbit.Login()
				if err != nil {
					a.Logger.Error().Err(err).Msg("app: Failed to login to qBittorrent")
				} else {
					a.Logger.Info().Msg("app: Logged in to qBittorrent")
				}
			}
		}()
		// Init Transmission
		trans, err := transmission.New(&transmission.NewTransmissionOptions{
			Logger:   a.Logger,
			Username: settings.Torrent.TransmissionUsername,
			Password: settings.Torrent.TransmissionPassword,
			Port:     settings.Torrent.TransmissionPort,
			Host:     settings.Torrent.TransmissionHost,
			Path:     settings.Torrent.TransmissionPath,
		})
		if err != nil && settings.Torrent.TransmissionUsername != "" && settings.Torrent.TransmissionPassword != "" { // Only log error if username and password are set
			a.Logger.Error().Err(err).Msg("app: Failed to initialize transmission client")
		}

		// Shutdown torrent client first
		if a.TorrentClientRepository != nil {
			a.TorrentClientRepository.Shutdown()
		}

		var builtInClient *seanime_torrent.Client
		if settings.Torrent.Default == torrent_client.SeanimeClient {
			builtInClient, err = seanime_torrent.New(&seanime_torrent.NewClientOptions{
				Logger:             a.Logger,
				Database:           a.Database,
				Dir:                a.Config.Torrent.Dir,
				Port:               settings.Torrent.SeanimePort,
				MaxConnections:     settings.Torrent.SeanimeMaxConnections,
				DownloadLimitKB:    settings.Torrent.SeanimeDownloadLimit,
				UploadLimitKB:      settings.Torrent.SeanimeUploadLimit,
				MaxActiveDownloads: settings.Torrent.SeanimeMaxActiveDownloads,
			})
			if err != nil {
				a.Logger.Error().Err(err).Msg("app: Failed to initialize Seanime torrent client")
			}
		}

		// Torrent Client Repository
		a.TorrentClientRepository = torrent_client.NewRepository(&torrent_client.NewRepositoryOptions{
			Logger:                 a.Logger,
			QbittorrentClient:      qbit,
			Transmission:           trans,
			SeanimeClient:          builtInClient,
			TorrentRepository:      a.TorrentRepository,
			Provider:               settings.Torrent.Default,
			MetadataProviderRef:    a.MetadataProviderRef,
			IsBuiltinClientEnabled: a.FeatureFlags.BuiltinTorrentClient,
		})

		a.TorrentClientRepository.InitActiveTorrentCount(settings.Torrent.ShowActiveTorrentCount, a.WSEventManager)

		// Set AutoDownloader qBittorrent client
		a.AutoDownloader.SetTorrentClientRepository(a.TorrentClientRepository)

		plugin.GlobalAppContext.SetModulesPartial(plugin.AppContextModules{
			TorrentClientRepository: a.TorrentClientRepository,
			AutoDownloader:          a.AutoDownloader,
		})
	} else {
		a.Logger.Warn().Msg("app: Did not initialize torrent client module, no settings found")
	}

	// +---------------------+
	// |   AutoDownloader    |
	// +---------------------+

	// Update Auto Downloader
	if settings.AutoDownloader != nil {
		go a.AutoDownloader.SetSettings(settings.AutoDownloader)
	}

	// +---------------------+
	// |   Library Watcher   |
	// +---------------------+

	// Initialize library watcher
	if settings.Library != nil && len(settings.Library.LibraryPath) > 0 {
		go a.initLibraryWatcher(settings.Library.GetLibraryPaths())
	}

	// +---------------------+
	// |       Discord       |
	// +---------------------+

	if settings.Discord != nil && a.DiscordPresence != nil {
		go a.DiscordPresence.SetSettings(settings.Discord)
	}

	// +---------------------+
	// |     Continuity      |
	// +---------------------+

	if settings.Library != nil {
		go a.ContinuityManager.SetSettings(&continuity.Settings{
			WatchContinuityEnabled: settings.Library.EnableWatchContinuity,
		})
	}

	if settings.Manga != nil {
		go a.MangaRepository.SetSettings(settings)
	}

	// +---------------------+
	// |       Nakama        |
	// +---------------------+

	if settings.Nakama != nil {
		a.NakamaManager.SetSettings(settings.Nakama)
	}

	a.Logger.Info().Msg("app: Refreshed modules")

}

// InitOrRefreshMediastreamSettings will initialize or refresh the mediastream settings.
// It is called after the App instance is created and after settings are updated.
func (a *App) InitOrRefreshMediastreamSettings() {

	var settings *models.MediastreamSettings
	var found bool
	settings, found = a.Database.GetMediastreamSettings()
	if !found {

		var err error
		settings, err = a.Database.UpsertMediastreamSettings(&models.MediastreamSettings{
			BaseModel: models.BaseModel{
				ID: 1,
			},
			TranscodeEnabled:    false,
			TranscodeHwAccel:    "cpu",
			TranscodePreset:     "fast",
			PreTranscodeEnabled: false,
		})
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to initialize mediastream module")
			return
		}
	}

	a.MediastreamRepository.InitializeModules(settings, a.Config.Cache.Dir, a.Config.Cache.TranscodeDir)

	// Cleanup cache
	go func() {
		if settings.TranscodeEnabled {
			// If transcoding is enabled, trim files
			_ = a.FileCacher.TrimMediastreamVideoFiles()
		} else {
			// If transcoding is disabled, clear all files
			_ = a.FileCacher.ClearMediastreamVideoFiles()
		}
	}()

	a.SecondarySettings.Mediastream = settings
}

// InitOrRefreshTorrentstreamSettings will initialize or refresh the mediastream settings.
// It is called after the App instance is created and after settings are updated.
func (a *App) InitOrRefreshTorrentstreamSettings() {

	var settings *models.TorrentstreamSettings
	var found bool
	settings, found = a.Database.GetTorrentstreamSettings()
	if !found {

		var err error
		settings, err = a.Database.UpsertTorrentstreamSettings(&models.TorrentstreamSettings{
			BaseModel: models.BaseModel{
				ID: 1,
			},
			Enabled:                   false,
			AutoSelect:                true,
			PreferredResolution:       "",
			DisableIPV6:               false,
			DownloadDir:               "",
			AddToLibrary:              false,
			TorrentClientHost:         "",
			TorrentClientPort:         43213,
			StreamingServerHost:       "0.0.0.0",
			StreamingServerPort:       43214,
			IncludeInLibrary:          false,
			StreamUrlAddress:          "",
			SlowSeeding:               false,
			PreloadNextStream:         false,
			DisableAcceleratedStartup: false,
		})
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to initialize mediastream module")
			return
		}
	}

	err := a.TorrentstreamRepository.InitModules(settings, a.Config.Server.Host, a.Config.Server.Port)
	if err != nil && settings.Enabled {
		a.Logger.Error().Err(err).Msg("app: Failed to initialize Torrent streaming module")
		//_, _ = a.Database.UpsertTorrentstreamSettings(&models.TorrentstreamSettings{
		//	BaseModel: models.BaseModel{
		//		ID: 1,
		//	},
		//	Enabled: false,
		//})
	}

	a.Cleanups = append(a.Cleanups, func() {
		a.TorrentstreamRepository.Shutdown()
	})

	// Set torrent streaming settings in secondary settings
	// so the client can use them
	a.SecondarySettings.Torrentstream = settings
}

func (a *App) InitOrRefreshDebridSettings() {

	settings, found := a.Database.GetDebridSettings()
	if !found {

		var err error
		settings, err = a.Database.UpsertDebridSettings(&models.DebridSettings{
			BaseModel: models.BaseModel{
				ID: 1,
			},
			Enabled:                      false,
			Provider:                     "",
			ApiKey:                       "",
			IncludeDebridStreamInLibrary: false,
			StreamAutoSelect:             false,
			StreamPreferredResolution:    "",
		})
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to initialize debrid module")
			return
		}
	}

	a.SecondarySettings.Debrid = settings

	err := a.DebridClientRepository.InitializeProvider(settings)
	if err != nil {
		a.Logger.Error().Err(err).Msg("app: Failed to initialize debrid provider")
		return
	}
}

// InitOrRefreshAnilistData will initialize the Anilist anime collection and the account.
// This function should be called after App.Database is initialized and after settings are updated.
func (a *App) InitOrRefreshAnilistData() {
	a.Logger.Debug().Msg("app: Fetching Anilist data")

	var currUser *user.User
	acc, err := a.Database.GetAccount()
	if err != nil || acc.Username == "" {
		a.ServerReady = true
		currUser = user.NewSimulatedUser() // Create a simulated user if no account is found
	} else {
		currUser, err = user.NewUser(acc)
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to create user from account")
			return
		}
	}

	a.user = currUser

	// Set username to Anilist platform
	a.AnilistPlatformRef.Get().SetUsername(currUser.Viewer.Name)

	a.Logger.Info().Msg("app: Authenticated to AniList")

	go func() {
		_, err = a.RefreshAnimeCollection()
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to fetch Anilist anime collection")
		}

		a.ServerReady = true
		a.WSEventManager.SendEvent(events.ServerReady, nil)

		_, err = a.RefreshMangaCollection()
		if err != nil {
			a.Logger.Error().Err(err).Msg("app: Failed to fetch Anilist manga collection")
		}
	}()

	go func(username string) {
		a.DiscordPresence.SetUsername(username)
	}(currUser.Viewer.Name)

	a.Logger.Info().Msg("app: Fetched Anilist data")
}

func (a *App) performActionsOnce() {

	go func() {
		if a.Settings == nil || a.Settings.Library == nil {
			return
		}

		if a.Settings.GetLibrary().OpenWebURLOnStart {
			// Open the web URL
			err := browser.OpenURL(a.Config.GetServerURI("127.0.0.1"))
			if err != nil {
				a.Logger.Warn().Err(err).Msg("app: Failed to open web URL, please open it manually in your browser")
			} else {
				a.Logger.Info().Msg("app: Opened web URL")
			}
		}

		if a.Settings.GetLibrary().RefreshLibraryOnStart {
			go func() {
				a.Logger.Debug().Msg("app: Refreshing library")
				a.AutoScanner.RunNow()
				a.Logger.Info().Msg("app: Refreshed library")
			}()
		}

		if a.Settings.GetLibrary().OpenTorrentClientOnStart && a.TorrentClientRepository != nil {
			// Start the torrent client
			ok := a.TorrentClientRepository.Start()
			if !ok {
				a.Logger.Warn().Msg("app: Failed to open torrent client")
			} else {
				a.Logger.Info().Msg("app: Started torrent client")
			}

		}
	}()

}
