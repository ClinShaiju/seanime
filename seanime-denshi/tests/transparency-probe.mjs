// Probes which BrowserWindow configuration actually yields a see-through window on this machine.
// Run: npx electron tests/transparency-probe.mjs   (env: PROBE_NOHW=1 disables hardware acceleration)
import { app, BrowserWindow, desktopCapturer, screen } from "electron"
import { writeFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const shotPath = process.env.PROBE_SHOT ?? path.join(scriptDir, "transparency-probe.png")

if (process.env.PROBE_NOHW) app.disableHardwareAcceleration()
if (process.env.PROBE_FLAGS) {
    for (const flag of process.env.PROBE_FLAGS.split(",")) {
        const [name, value] = flag.split("=")
        app.commandLine.appendSwitch(name, value)
    }
}

const html = label => `data:text/html,${encodeURIComponent(`
<body style="margin:0;background:transparent">
  <div style="position:absolute;left:10px;top:10px;padding:8px 12px;background:rgba(220,0,80,.9);color:#fff;
              font:20px sans-serif;border-radius:8px">${label}</div>
</body>`)}`

const VARIANTS = [
    { label: "A no-bgcolor", options: {} },
    { label: "B bgcolor-00", options: { backgroundColor: "#00000000" } },
    { label: "C no-shadow-thin", options: { backgroundColor: "#00000000", hasShadow: false, thickFrame: false, roundedCorners: false } },
    { label: "D set-after-load", options: {}, setAfterLoad: true },
]

function delay(ms) {
    return new Promise(resolve => setTimeout(resolve, ms))
}

app.whenReady().then(async () => {
    const display = screen.getPrimaryDisplay()
    const windows = []

    for (const [index, variant] of VARIANTS.entries()) {
        const window = new BrowserWindow({
            x: 60 + index * 340, y: 120, width: 320, height: 220,
            frame: false, transparent: true, show: false,
            webPreferences: { sandbox: true },
            ...variant.options,
        })
        await window.loadURL(html(variant.label))
        if (variant.setAfterLoad) window.setBackgroundColor("#00000000")
        window.show()
        windows.push(window)
    }

    await delay(1500)
    const sources = await desktopCapturer.getSources({
        types: ["screen"],
        thumbnailSize: { width: display.size.width, height: display.size.height },
    })
    writeFileSync(shotPath, sources[0].thumbnail.toPNG())
    console.log(`PROBE_SHOT ${shotPath}`)
    console.log("PROBE_GPU", JSON.stringify({
        noHardwareAcceleration: !!process.env.PROBE_NOHW,
        flags: process.env.PROBE_FLAGS ?? "",
    }))
    app.exit(0)
}).catch(error => {
    console.error("PROBE_FAILED", error)
    app.exit(1)
})
