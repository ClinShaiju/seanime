// Integration check for the real native backend module (dist/main/mpv-native/session.js): starts a session
// against a stand-in UI window, plays a synthetic clip and asserts that display sync engages.
// Run: npm run build:main && npx electron tests/mpv-native-session.mjs
import { app, BrowserWindow, desktopCapturer, screen } from "electron"
import { spawnSync } from "node:child_process"
import { createRequire } from "node:module"
import { existsSync, writeFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const require = createRequire(import.meta.url)
const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const { MpvNativeSession } = require(path.join(scriptDir, "..", "dist", "main", "mpv-native", "session.js"))
const shotPath = process.env.SESSION_SHOT ?? path.join(scriptDir, "mpv-native-session.png")

const UI_HTML = `data:text/html,${encodeURIComponent(`
<body style="margin:0;background:transparent;font:20px sans-serif;color:#fff">
  <div style="position:absolute;left:24px;top:24px;padding:10px 16px;background:rgba(220,0,80,.85);
              border-radius:10px">SESSION UI OVERLAY</div>
</body>`)}`

function delay(ms) {
    return new Promise(resolve => setTimeout(resolve, ms))
}

/** lavfi sources are not seekable, so the seek assertions need a real file; mpv can encode one itself. */
function ensureTestClip() {
    const clip = path.join(app.getPath("temp"), "seanime-mpv-native-test.mp4")
    if (existsSync(clip)) return clip
    const mpv = path.join(scriptDir, "..", "binaries", "mpv", "mpv.exe")
    const result = spawnSync(mpv, [
        "av://lavfi:testsrc2=size=640x360:rate=24", "--frames=480", "--ovc=libx264", "--no-terminal", `--o=${clip}`,
    ])
    if (result.status !== 0 || !existsSync(clip)) throw new Error("could not encode the test clip")
    return clip
}

const failures = []

function check(name, condition, detail) {
    console.log(`${condition ? "PASS" : "FAIL"} ${name}${detail === undefined ? "" : ` (${detail})`}`)
    if (!condition) failures.push(name)
}

app.whenReady().then(async () => {
    const display = screen.getPrimaryDisplay()
    const uiWindow = new BrowserWindow({
        x: 100, y: 100, width: 1280, height: 720,
        frame: false, transparent: true, backgroundColor: "#00000000",
        webPreferences: { sandbox: true },
    })
    await uiWindow.loadURL(UI_HTML)
    uiWindow.show()

    const events = []
    const session = await MpvNativeSession.create(
        "seanime-mpv-core-test",
        uiWindow,
        {
            options: { "video-sync": "display-resample", interpolation: "yes", tscale: "oversample" },
            observe: ["time-pos", "pause", "track-list"],
        },
        event => events.push(event),
        reason => console.log("SESSION_EXIT", reason),
    )

    session.setVideoRect({ x: 0, y: 0, width: 1280, height: 720 })
    await session.command(["loadfile", ensureTestClip(), "replace"])
    await delay(3000)

    const displaySyncActive = await session.getProperty("display-sync-active")
    const vsyncRatio = await session.getProperty("vsync-ratio")
    const timePos = await session.getProperty("time-pos")
    const videoSync = await session.getProperty("video-sync")
    const interpolation = await session.getProperty("interpolation")
    const displayFps = await session.getProperty("display-fps")

    check("session plays", typeof timePos === "number" && timePos > 0.5, `time-pos=${timePos}`)
    check("display sync active", displaySyncActive === true)
    check("video-sync applied", videoSync === "display-resample", videoSync)
    check("interpolation on", interpolation === true)
    check("display fps matches monitor", displayFps === (display.displayFrequency || 60), `mpv=${displayFps} electron=${display.displayFrequency}`)
    check("vsync ratio sane", typeof vsyncRatio === "number" && vsyncRatio > 0, `ratio=${vsyncRatio}`)
    check("property events flowing", events.some(event => event.event === "property-change" && event.name === "time-pos"),
        `${events.length} events`)
    check("file-loaded delivered", events.some(event => event.event === "file-loaded"))

    // Pause/seek round trip through the same path the renderer uses
    await session.setProperty("pause", true)
    await delay(300)
    check("pause applied", await session.getProperty("pause") === true)
    await session.command(["seek", 12, "absolute+exact"])
    await delay(500)
    const seeked = await session.getProperty("time-pos")
    check("absolute seek applied", typeof seeked === "number" && Math.abs(seeked - 12) < 0.5, `time-pos=${seeked}`)
    await session.command(["seek", -3, "relative+exact"])
    await delay(500)
    const relative = await session.getProperty("time-pos")
    check("relative seek applied", typeof relative === "number" && Math.abs(relative - 9) < 0.5, `time-pos=${relative}`)

    const sources = await desktopCapturer.getSources({
        types: ["screen"],
        thumbnailSize: { width: display.size.width, height: display.size.height },
    })
    if (sources[0]) {
        writeFileSync(shotPath, sources[0].thumbnail.toPNG())
        console.log(`SESSION_SHOT ${shotPath}`)
    }

    await session.destroy()
    check("ui window survives teardown", !uiWindow.isDestroyed())

    console.log(failures.length ? `SESSION_RESULT FAIL: ${failures.join(", ")}` : "SESSION_RESULT PASS")
    app.exit(failures.length ? 1 : 0)
}).catch(error => {
    console.error("SESSION_FAILED", error)
    app.exit(1)
})
