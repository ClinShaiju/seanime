// Architecture spike for the native mpv backend: proves that mpv renders into an Electron-owned window and
// that a transparent child window composites the UI on top of it. Run: npx electron tests/mpv-native-spike.mjs
import { app, BrowserWindow, desktopCapturer, screen } from "electron"
import { spawn } from "node:child_process"
import { writeFileSync } from "node:fs"
import net from "node:net"
import path from "node:path"
import { fileURLToPath } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const mpvBinary = path.join(scriptDir, "..", "binaries", "mpv", "mpv.exe")
const shotPath = process.env.SPIKE_SHOT ?? path.join(scriptDir, "mpv-native-spike.png")
const pipePath = `\\\\.\\pipe\\seanime-mpv-spike-${process.pid}`

const OVERLAY_HTML = `data:text/html,${encodeURIComponent(`
<body style="margin:0;background:transparent;font-family:sans-serif">
  <div style="position:absolute;inset:0;background:transparent"></div>
  <div style="position:absolute;left:40px;top:40px;padding:16px 24px;background:rgba(220,0,80,.85);
              color:#fff;font-size:28px;border-radius:12px">OVERLAY ON TOP</div>
  <div style="position:absolute;left:0;right:0;bottom:0;height:90px;background:linear-gradient(transparent,rgba(0,0,0,.85))"></div>
</body>`)}`

function delay(ms) {
    return new Promise(resolve => setTimeout(resolve, ms))
}

async function connectIpc(attempts = 60) {
    for (let attempt = 0; attempt < attempts; attempt++) {
        try {
            return await new Promise((resolve, reject) => {
                const socket = net.connect(pipePath)
                socket.once("connect", () => resolve(socket))
                socket.once("error", reject)
            })
        }
        catch {
            await delay(100)
        }
    }
    throw new Error("could not connect to the mpv ipc pipe")
}

async function main() {
    const display = screen.getPrimaryDisplay()
    const bounds = { x: 80, y: 80, width: 1280, height: 720 }

    const uiOnly = !!process.env.SPIKE_UIONLY
    const videoWindow = new BrowserWindow({
        ...bounds, frame: false, title: "spike-video", skipTaskbar: true,
        // A Chromium-painted host fights mpv's child HWND for the surface; a transparent host paints nothing
        ...(process.env.SPIKE_TRANSPARENT_HOST
            ? { transparent: true, backgroundColor: "#00000000" }
            : { backgroundColor: "#000000" }),
        webPreferences: { sandbox: true },
    })
    if (!uiOnly) videoWindow.show()

    const handle = videoWindow.getNativeWindowHandle()
    const wid = (handle.length >= 8 ? handle.readBigUInt64LE(0) : BigInt(handle.readUInt32LE(0))).toString()

    const uiWindow = new BrowserWindow({
        ...bounds, parent: uiOnly ? undefined : videoWindow, frame: false, transparent: true, backgroundColor: "#00000000",
        hasShadow: false, webPreferences: { sandbox: true },
    })
    uiWindow.webContents.on("did-fail-load", (_e, code, desc) => console.log("SPIKE_UI_LOAD_FAIL", code, desc))
    await uiWindow.loadURL(OVERLAY_HTML)
    uiWindow.show()
    console.log("SPIKE_UI", JSON.stringify({
        visible: uiWindow.isVisible(),
        bounds: uiWindow.getBounds(),
        url: uiWindow.webContents.getURL().slice(0, 60),
        alwaysOnTop: uiWindow.isAlwaysOnTop(),
    }))

    if (process.env.SPIKE_NOMPV || uiOnly) {
        await delay(2000)
        const shots = await desktopCapturer.getSources({
            types: ["screen"],
            thumbnailSize: { width: display.size.width, height: display.size.height },
        })
        writeFileSync(shotPath, shots[0].thumbnail.toPNG())
        console.log(`SPIKE_SHOT ${shotPath}`)
        return app.exit(0)
    }

    const mpv = spawn(mpvBinary, [
        `--wid=${wid}`,
        `--input-ipc-server=${pipePath}`,
        "--idle=yes",
        "--force-window=yes",
        "--keep-open=yes",
        "--no-terminal",
        "--no-config",
        "--no-input-default-bindings",
        "--input-vo-keyboard=no",
        "--osc=no",
        "--osd-level=0",
        `--display-fps-override=${display.displayFrequency || 60}`,
        "--video-sync=display-resample",
        "--interpolation=yes",
        "--tscale=oversample",
        ...(process.env.SPIKE_NOFLIP ? ["--gpu-context=d3d11", "--d3d11-flip=no"] : []),
        "av://lavfi:testsrc2=size=1280x720:rate=24",
    ], { stdio: ["ignore", "ignore", "pipe"] })
    mpv.stderr.setEncoding("utf8")
    mpv.stderr.on("data", chunk => process.stdout.write(`[mpv] ${chunk}`))

    const socket = await connectIpc()
    socket.setEncoding("utf8")
    const answers = new Map()
    socket.on("data", chunk => {
        for (const line of chunk.split("\n")) {
            if (!line.trim()) continue
            try {
                const message = JSON.parse(line)
                if (typeof message.request_id === "number") answers.set(message.request_id, message)
            }
            catch {
                // events we do not care about here
            }
        }
    })

    let requestId = 0
    const ask = async (command) => {
        const id = ++requestId
        socket.write(`${JSON.stringify({ command, request_id: id })}\n`)
        for (let attempt = 0; attempt < 50; attempt++) {
            if (answers.has(id)) return answers.get(id)
            await delay(100)
        }
        return { error: "timeout" }
    }

    await delay(1500)
    if (process.env.SPIKE_ONTOP) uiWindow.setAlwaysOnTop(true)
    if (process.env.SPIKE_MOVETOP) uiWindow.moveTop()
    await delay(1500)

    const report = {
        displaySyncActive: await ask(["get_property", "display-sync-active"]),
        videoSync: await ask(["get_property", "video-sync"]),
        interpolation: await ask(["get_property", "interpolation"]),
        estimatedDisplayFps: await ask(["get_property", "display-fps"]),
        estimatedVfFps: await ask(["get_property", "estimated-vf-fps"]),
        vsyncRatio: await ask(["get_property", "vsync-ratio"]),
        hwdec: await ask(["get_property", "hwdec-current"]),
        timePos: await ask(["get_property", "time-pos"]),
    }
    console.log("SPIKE_REPORT", JSON.stringify(report, null, 2))

    console.log("SPIKE_UI_LATE", JSON.stringify({ visible: uiWindow.isVisible(), bounds: uiWindow.getBounds() }))
    writeFileSync(shotPath.replace(".png", "-uiwindow.png"), (await uiWindow.capturePage()).toPNG())

    const sources = await desktopCapturer.getSources({
        types: ["screen"],
        thumbnailSize: { width: display.size.width, height: display.size.height },
    })
    if (sources[0]) {
        writeFileSync(shotPath, sources[0].thumbnail.toPNG())
        console.log(`SPIKE_SHOT ${shotPath}`)
    }

    try {
        await ask(["quit"])
    }
    catch {
        // mpv may already be gone
    }
    mpv.kill()
    app.exit(0)
}

app.whenReady().then(main).catch(error => {
    console.error("SPIKE_FAILED", error)
    app.exit(1)
})
