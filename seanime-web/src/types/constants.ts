export const __isElectronDesktop__ = import.meta.env.SEA_PUBLIC_DESKTOP === "electron"
export const __isDesktop__ = import.meta.env.SEA_PUBLIC_PLATFORM === "desktop" || __isElectronDesktop__
export const __clientPlatform__ = __isElectronDesktop__
    ? "denshi"
    : import.meta.env.SEA_PUBLIC_PLATFORM === "web"
        ? "web"
        : import.meta.env.SEA_PUBLIC_PLATFORM === "mobile"
            ? "mobile"
            : ""
// Version baked into the web bundle at build time (passed as SEA_BUILD_VERSION,
// mirrors the Go server's constants.Version). Empty in dev. Used to detect a tab
// running stale JS after the server was updated under it.
export const __buildVersion__ = import.meta.env.SEA_BUILD_VERSION || ""

export const HIDE_IMAGES = false

export const __CAST_ENABLED__ = false

