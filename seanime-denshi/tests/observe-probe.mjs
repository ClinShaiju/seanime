// Do display-sync properties actually emit property-change events, or only answer getProperty?
import { app, BrowserWindow } from "electron"
import { spawnSync } from "node:child_process"
import { createRequire } from "node:module"
import { existsSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const require = createRequire(import.meta.url)
const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const { MpvNativeSession } = require(path.join(scriptDir, "..", "dist", "main", "mpv-native", "session.js"))
const delay = ms => new Promise(r => setTimeout(r, ms))

const WATCHED = ["display-fps", "display-sync-active", "vsync-ratio", "vsync-jitter", "estimated-display-fps"]

app.whenReady().then(async () => {
    const uiWindow = new BrowserWindow({ x: 100, y: 100, width: 1280, height: 720,
        frame: false, transparent: true, backgroundColor: "#00000000", webPreferences: { sandbox: true } })
    await uiWindow.loadURL("data:text/html,<body style=background:transparent>")
    uiWindow.show()

    const clip = path.join(app.getPath("temp"), "seanime-mpv-native-test.mp4")
    if (!existsSync(clip)) {
        spawnSync(path.join(scriptDir, "..", "binaries", "mpv", "mpv.exe"),
            ["av://lavfi:testsrc2=size=640x360:rate=24", "--frames=480", "--ovc=libx264", "--no-terminal", "--o=" + clip])
    }

    const seen = new Map()
    const session = await MpvNativeSession.create("observe-probe", uiWindow,
        { options: { "video-sync": "display-resample", interpolation: "yes" }, observe: WATCHED },
        event => {
            if (event.event !== "property-change" || !event.name) return
            if (!WATCHED.includes(event.name)) return
            const list = seen.get(event.name) ?? []
            list.push(event.data)
            seen.set(event.name, list)
        },
        () => {})
    session.setVideoRect({ x: 0, y: 0, width: 1280, height: 720 })
    await session.command(["loadfile", clip, "replace"])
    await delay(10000)

    console.log("")
    console.log("property                observe events                 getProperty")
    for (const name of WATCHED) {
        const events = seen.get(name) ?? []
        const polled = await session.getProperty(name).catch(e => "ERR " + e.message)
        const shown = events.length ? `${events.length}x last=${JSON.stringify(events[events.length - 1])}` : "NONE"
        console.log(name.padEnd(24) + shown.padEnd(31) + JSON.stringify(polled))
    }
    await session.destroy()
    app.exit(0)
})
