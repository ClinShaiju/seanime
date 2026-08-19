// Determines which seek command form mpv 0.41 actually honours, since the player relies on absolute seeks.
// Run: npx electron tests/mpv-seek-probe.mjs
import { app, BrowserWindow } from "electron"
import { createRequire } from "node:module"
import path from "node:path"
import { fileURLToPath } from "node:url"

const require = createRequire(import.meta.url)
const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const { MpvNativeSession } = require(path.join(scriptDir, "..", "dist", "main", "mpv-native", "session.js"))

function delay(ms) {
    return new Promise(resolve => setTimeout(resolve, ms))
}

app.whenReady().then(async () => {
    const uiWindow = new BrowserWindow({
        x: 100, y: 100, width: 640, height: 360, frame: false, transparent: true,
        backgroundColor: "#00000000", show: false, webPreferences: { sandbox: true },
    })

    const session = await MpvNativeSession.create("seek-probe", uiWindow, {}, () => {}, () => {})
    const media = process.env.PROBE_MEDIA ?? "av://lavfi:testsrc2=size=640x360:rate=24:duration=60"
    await session.command(["loadfile", media, "replace"])
    await delay(2000)
    await session.setProperty("pause", true)

    const forms = [
        ["seek", 12, "absolute+exact"],
        ["seek", 12, "absolute"],
        ["seek", -3, "relative+exact"],
        ["set_property", "time-pos", 12],
    ]

    for (const form of forms) {
        let resetError = null
        await session.command(["seek", 5, "absolute+exact"]).catch(e => { resetError = e.message })
        await delay(400)
        const before = await session.getProperty("time-pos")
        const paused = await session.getProperty("pause")
        const idle = await session.getProperty("core-idle")
        let error = null
        try {
            await session.command(form)
        }
        catch (thrown) {
            error = thrown.message
        }
        await delay(400)
        const after = await session.getProperty("time-pos")
        console.log(`FORM ${JSON.stringify(form)} before=${before?.toFixed?.(2)} after=${after?.toFixed?.(2)} paused=${paused} idle=${idle} resetError=${resetError} error=${error}`)
    }

    await session.destroy()
    app.exit(0)
}).catch(error => {
    console.error("PROBE_FAILED", error)
    app.exit(1)
})
