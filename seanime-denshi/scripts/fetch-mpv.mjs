// Downloads a Windows mpv build into binaries/mpv/ so Denshi can ship a real mpv process.
// The native playback backend drives this binary over a JSON IPC pipe; see mpv-native-window.md.
import { createWriteStream } from "node:fs"
import { mkdir, readdir, rm, stat } from "node:fs/promises"
import { spawnSync } from "node:child_process"
import path from "node:path"
import { fileURLToPath } from "node:url"
import { Readable } from "node:stream"
import { pipeline } from "node:stream/promises"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const denshiDir = path.resolve(scriptDir, "..")
const outDir = path.join(denshiDir, "binaries", "mpv")
const releasesUrl = "https://api.github.com/repos/shinchiro/mpv-winbuild-cmake/releases"

async function exists(target) {
    try {
        await stat(target)
        return true
    }
    catch {
        return false
    }
}

async function findAsset() {
    const response = await fetch(releasesUrl, { headers: { "user-agent": "seanime-denshi", accept: "application/vnd.github+json" } })
    if (!response.ok) throw new Error(`GitHub API returned ${response.status}`)
    const releases = await response.json()
    for (const release of releases) {
        // mpv-dev-* is the libmpv SDK, mpv-x86_64-v3-* needs AVX2 — take the baseline player build
        const asset = release.assets?.find(item => /^mpv-x86_64-\d.*\.7z$/.test(item.name))
        if (asset) return asset
    }
    throw new Error("no mpv-x86_64 asset found in the latest releases")
}

async function main() {
    const force = process.argv.includes("--force")
    if (!force && await exists(path.join(outDir, "mpv.exe"))) {
        console.log(`[fetch-mpv] mpv.exe already present in ${outDir}`)
        return
    }

    const asset = await findAsset()
    console.log(`[fetch-mpv] downloading ${asset.name} (${(asset.size / 1e6).toFixed(1)} MB)`)

    await mkdir(outDir, { recursive: true })
    const archivePath = path.join(outDir, asset.name)
    const download = await fetch(asset.browser_download_url, { headers: { "user-agent": "seanime-denshi" } })
    if (!download.ok || !download.body) throw new Error(`download failed with ${download.status}`)
    await pipeline(Readable.fromWeb(download.body), createWriteStream(archivePath))

    // bsdtar ships with Windows 10+ and reads 7z through libarchive, so no extra dependency is needed.
    // It has to be the System32 one: a `tar` from PATH may be GNU tar, which reads "H:\..." as a remote host.
    const tarBinary = process.platform === "win32"
        ? path.join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe")
        : "tar"
    const extract = spawnSync(tarBinary, ["-xf", archivePath, "-C", outDir], { stdio: "inherit" })
    if (extract.status !== 0) throw new Error(`extraction failed (tar exited with ${extract.status})`)
    await rm(archivePath, { force: true })

    // The build ships everything under binaries/, so drop what the player never uses
    for (const extra of ["doc", "installer", "mpv", "mpv.com", "mpv-register.bat", "mpv-unregister.bat", "updater.bat"]) {
        await rm(path.join(outDir, extra), { recursive: true, force: true })
    }

    if (!await exists(path.join(outDir, "mpv.exe"))) {
        throw new Error(`extraction produced no mpv.exe (contents: ${(await readdir(outDir)).join(", ")})`)
    }
    console.log(`[fetch-mpv] ready: ${path.join(outDir, "mpv.exe")}`)
}

main().catch(error => {
    console.error(`[fetch-mpv] ${error.message}`)
    process.exit(1)
})
