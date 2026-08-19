// Integration check for the real native backend module (dist/main/mpv-native/session.js): starts a session
// against a stand-in UI window, plays a synthetic clip and asserts that display sync engages.
// Run: npm run build:main && npx electron tests/mpv-native-session.mjs
import { app, BrowserWindow, desktopCapturer, screen } from "electron"
import { spawnSync } from "node:child_process"
import { createRequire } from "node:module"
import { appendFileSync, existsSync, writeFileSync } from "node:fs"
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

/** Minimal valid mpv user shader, enough to prove the glsl-shaders list actually takes a path. */
function ensureTestShader() {
    const shader = path.join(app.getPath("temp"), "seanime-mpv-native-test.glsl")
    writeFileSync(shader, `//!HOOK MAIN
//!BIND HOOKED
//!DESC identity
vec4 hook() { return HOOKED_tex(HOOKED_pos); }
`)
    return shader
}

const traceFile = process.env.SESSION_TRACE
function trace(step) {
    if (traceFile) appendFileSync(traceFile, step + "\n")
}

const failures = []

function check(name, condition, detail) {
    trace(`check:${name}`)
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
            observe: ["time-pos", "pause", "track-list", "eof-reached"],
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
    // Interpolation is now conditional: it only earns its cost when the display/video FPS ratio is far from
    // a whole number. A 24 fps clip on a 144 Hz panel is an even 6, so it must be OFF here - leaving it on
    // makes mpv render every vsync instead of every frame, which is what buried Anime4K in dropped frames.
    const containerFps = await session.getProperty("container-fps")
    const ratio = displayFps / containerFps
    const expectInterpolation = Math.abs(ratio - Math.round(ratio)) > 0.05
    check("interpolation matches the cadence", interpolation === expectInterpolation,
        `interpolation=${interpolation} expected=${expectInterpolation} ratio=${ratio.toFixed(3)}`)
    // No --display-fps-override is passed any more: mpv measures the true rate itself even through --wid.
    // Electron only ever reports a rounded integer, so mpv should land near it but NOT on it.
    check("mpv detects the real refresh rate", Math.abs(displayFps - (display.displayFrequency || 60)) < 1,
        `mpv=${displayFps} electron=${display.displayFrequency}`)
    check("vsync ratio sane", typeof vsyncRatio === "number" && vsyncRatio > 0, `ratio=${vsyncRatio}`)
    // display-fps-override is handed to mpv from Electron's (integer) display frequency, so a panel that is
    // really 59.94 Hz is told 60. mpv trusts the number instead of measuring, and the lie shows up here:
    // jitter is the relative stddev of actual vsync intervals, and mistimed frames climb when it is wrong.
    const vsyncJitter = await session.getProperty("vsync-jitter")
    const mistimed = await session.getProperty("mistimed-frame-count")
    check("vsync jitter low", typeof vsyncJitter === "number" && vsyncJitter < 0.02,
        `jitter=${vsyncJitter} mistimed=${mistimed}`)
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

    trace("shaders:start")
    // Anime4K goes through these exact commands (mirrored from mpv-prism): clr, then one append per path
    const shader = ensureTestShader()
    await session.command(["change-list", "glsl-shaders", "clr", ""])
    await session.command(["change-list", "glsl-shaders", "append", shader])
    trace("shaders:appended")
    const shaderList = await session.getProperty("glsl-shaders")
    trace(`shaders:list=${JSON.stringify(shaderList)}`)
    check("shader appended", Array.isArray(shaderList) && shaderList.length === 1, JSON.stringify(shaderList))
    await session.command(["change-list", "glsl-shaders", "clr", ""])
    const clearedList = await session.getProperty("glsl-shaders")
    check("shaders cleared", Array.isArray(clearedList) && clearedList.length === 0, JSON.stringify(clearedList))

    // mpv's detected rate and its own measurement of actual vsync intervals must agree. They diverge when
    // something forces a wrong rate on it, which is exactly what --display-fps-override used to do.
    await delay(7000)
    const measured = await session.getProperty("estimated-display-fps")
    const detected = await session.getProperty("display-fps")
    check("detected refresh rate matches the measured one",
        typeof measured === "number" && Math.abs(detected - measured) < 0.5,
        `display-fps=${detected} estimated=${measured}`)

    trace("eof:start")
    // Regression guard for auto-next. The session runs mpv with --keep-open=yes, which makes it pause on the
    // last frame and NEVER emit `end-file` (measured: end-file only arrives with --keep-open=no), so
    // `eof-reached` is the only end-of-file signal the renderer can act on. If this stops arriving, playback
    // silently stops at the end of every episode instead of advancing.
    const eofBefore = events.filter(event => event.event === "end-file").length
    await session.setProperty("pause", false)
    await session.command(["seek", 19.5, "absolute+exact"])
    await delay(3000)
    const eofEvents = events.filter(event => event.event === "property-change" && event.name === "eof-reached")
    check("eof-reached observed at end of file", eofEvents.some(event => event.data === true),
        JSON.stringify(eofEvents.map(event => event.data)))
    check("keep-open still suppresses end-file",
        events.filter(event => event.event === "end-file").length === eofBefore,
        "end-file must not be relied on for auto-next")

    trace("clip:start")
    // The mini player relies on a clip-path with a fill rule; verify Chromium actually parses it
    const clipPath = await uiWindow.webContents.executeJavaScript(`(() => {
        const el = document.createElement("div")
        el.style.clipPath = "polygon(evenodd, 0 0, 100% 0, 100% 100%, 0 100%, 0 0, 10px 10px, 10px 20px, 20px 20px, 20px 10px, 10px 10px)"
        document.body.appendChild(el)
        const value = getComputedStyle(el).clipPath
        el.remove()
        return value
    })()`)
    check("clip-path hole supported", typeof clipPath === "string" && clipPath.includes("evenodd"), clipPath)

    trace("capture:start")
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
