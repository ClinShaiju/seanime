// The fork versions Denshi as X.Y.Z.N (upstream X.Y.Z + fork revision N), but package.json — and
// therefore app.getVersion(), latest.yml and every electron-updater comparison — has to hold valid
// semver, so the version is stored as "X.Y.Z-N". See electron-builder.config.js for the full
// rationale; this is the display side of the same mapping.
const FORK_VERSION_RE = /^(\d+\.\d+\.\d+)-(\d+)$/

/** "3.10.2-2" -> "3.10.2.2". Anything else (upstream's plain semver) is returned unchanged. */
export function toDisplayVersion(version: string | undefined | null): string {
    if (!version) return ""
    const match = FORK_VERSION_RE.exec(version)
    return match ? `${match[1]}.${match[2]}` : version
}
