// electron-builder configuration.
//
// This lives here rather than in package.json's "build" field because the fork uses a 4-segment
// version (3.10.2.2 = upstream 3.10.2 + fork revision 2) that npm/electron-builder reject: they run
// the package.json version through normalize-package-data, which throws `Invalid version` on
// anything that is not valid semver.
//
// So the version is stored in package.json in its semver form, "X.Y.Z-N", and the 4-segment form is
// derived here and exposed as `buildVersion` (used by the artifactName templates below and, on
// Windows, as the file version). Keeping package.json semver is what lets electron-updater work at
// all — it parses both the running app's version and latest.yml's version with semver and throws
// ERR_UPDATER_INVALID_VERSION otherwise. Precedence still works out: 3.10.2-2 < 3.10.2-3 < 3.11.0-1.
//
// NOTE: "X.Y.Z-N" sorts BELOW plain "X.Y.Z", so every fork release must carry a fork revision;
// releasing a plain X.Y.Z would strand it above the fork builds that follow it. assertForkVersion
// below enforces that.
const pkg = require("./package.json")

const FORK_VERSION_RE = /^(\d+\.\d+\.\d+)-(\d+)$/

// "3.10.2-2" -> "3.10.2.2"
function toDisplayVersion(version) {
    const match = FORK_VERSION_RE.exec(version)
    return match ? `${match[1]}.${match[2]}` : version
}

function assertForkVersion(version) {
    if (!FORK_VERSION_RE.test(version)) {
        throw new Error(
            `seanime-denshi/package.json version "${version}" is not a fork version.\n` +
            `Expected "X.Y.Z-N" (the semver encoding of the 4-segment fork version X.Y.Z.N), e.g. "3.10.2-2" for 3.10.2.2.\n` +
            `A plain "X.Y.Z" would sort above every X.Y.Z-N build and break auto-update for the releases after it.`,
        )
    }
}

assertForkVersion(pkg.version)

module.exports = {
    // Display (4-segment) version. Artifact names use it so the release assets match the git tag
    // (v3.10.2.2), which the release workflow verifies.
    buildVersion: toDisplayVersion(pkg.version),
    electronDownload: {
        mirror: "https://seanime.app/assets/electron/",
        customDir: "v42.4.0",
    },
    appId: "app.seanime.denshi",
    productName: "Seanime Denshi",
    asar: true,
    extraResources: [
        {
            from: "binaries",
            to: "binaries",
        },
        {
            from: "native-builds",
            to: "native-builds",
        },
    ],
    generateUpdatesFilesForAllChannels: true,
    // The fork revision is encoded as a semver prerelease ("3.10.2-2"), and electron-builder would
    // otherwise read that prerelease tag as an update channel and publish "2.yml" instead of
    // "latest.yml". The explicit publish.channel below already prevents it; this makes it explicit.
    detectUpdateChannel: false,
    publish: {
        provider: "generic",
        url: "https://github.com/ClinShaiju/seanime/releases/latest/download",
        channel: "latest",
        publishAutoUpdate: true,
        useMultipleRangeRequest: false,
    },
    mac: {
        category: "public.app-category.entertainment",
        target: [
            {
                target: "default",
                arch: ["arm64"],
            },
        ],
        darkModeSupport: true,
        notarize: false,
        hardenedRuntime: true,
        gatekeeperAssess: false,
        entitlements: "assets/entitlements.mac.plist",
        entitlementsInherit: "assets/entitlements.mac.plist",
        artifactName: "seanime-denshi-${buildVersion}_MacOS_${arch}.${ext}",
        identity: "-",
    },
    win: {
        target: [
            {
                target: "nsis",
                arch: ["x64"],
            },
        ],
        artifactName: "seanime-denshi-${buildVersion}_Windows_${arch}.${ext}",
        verifyUpdateCodeSignature: false,
    },
    linux: {
        target: [
            {
                target: "AppImage",
                arch: ["x64"],
            },
        ],
        category: "Entertainment",
        artifactName: "seanime-denshi-${buildVersion}_Linux_${arch}.${ext}",
        desktop: {
            StartupWMClass: "seanime-denshi",
        },
    },
    nsis: {
        oneClick: false,
        allowToChangeInstallationDirectory: true,
        createDesktopShortcut: true,
        createStartMenuShortcut: true,
        deleteAppDataOnUninstall: true,
        shortcutName: "Seanime Denshi",
        artifactName: "seanime-denshi-${buildVersion}_Windows_${arch}.${ext}",
        perMachine: true,
    },
    directories: {
        buildResources: "assets",
        output: "dist",
    },
    files: [
        "dist/main/**/*",
        "src/**/*",
        "!src/main/**/*.ts",
        "web-denshi/**/*",
        "assets/**/*",
        "package.json",
        "!**/node_modules/*/{CHANGELOG.md,README.md,README,readme.md,readme}",
        "!**/node_modules/*/{test,__tests__,tests,powered-test,example,examples}",
        "!**/node_modules/*.d.ts",
        "!**/node_modules/.bin",
        "!**/*.{iml,o,hprof,orig,pyc,pyo,rbc,swp,csproj,sln,xproj}",
        "!.editorconfig",
        "!**/._*",
        "!**/{.DS_Store,.git,.hg,.svn,CVS,RCS,SCCS,.gitignore,.gitattributes}",
        "!**/{__pycache__,thumbs.db,.flowconfig,.idea,.vs,.nyc_output}",
        "!**/{appveyor.yml,.travis.yml,circle.yml}",
        "!**/{npm-debug.log,yarn.lock,.yarn-integrity,.yarn-metadata.json}",
    ],
}
