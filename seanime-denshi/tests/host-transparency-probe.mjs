// Is the erratic present pacing caused by hosting mpv in a TRANSPARENT window? Runs the same clip through
// --wid into a transparent host, then an opaque one, and reports mpv's own pacing counters for each.
import { app, BrowserWindow, screen } from "electron"
import { spawn } from "node:child_process"
import net from "node:net"
import os from "node:os"
import path from "node:path"
import { existsSync, writeFileSync, appendFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const MPV = path.join(scriptDir, "..", "binaries", "mpv", "mpv.exe")
const clip = path.join(os.tmpdir(), "seanime-mpv-native-test.mp4")
const delay = ms => new Promise(r => setTimeout(r, ms))
const OUT = "H:/Projects/seanime/seanime-denshi/tests/host-transparency-result.txt"
writeFileSync(OUT, "")
const say = (...parts) => appendFileSync(OUT, parts.join(" ") + String.fromCharCode(10))
const B = String.fromCharCode(92)
const NL = String.fromCharCode(10)

function widOf(win) {
    const h = win.getNativeWindowHandle()
    return (h.length >= 8 ? h.readBigUInt64LE(0) : BigInt(h.readUInt32LE(0))).toString()
}

async function run(label, transparent, withOverlay) {
    const display = screen.getPrimaryDisplay()
    const b = display.bounds
    const win = new BrowserWindow({
        x: b.x, y: b.y, width: b.width, height: b.height,
        show: false, frame: false, transparent,
        backgroundColor: transparent ? "#00000000" : "#000000",
        focusable: false, skipTaskbar: true,
        webPreferences: { sandbox: true },
    })
    win.setMenu(null)
    win.showInactive()

    // Reproduces the app's shape: a transparent UI window sitting directly on top of the video window.
    let overlay = null
    if (withOverlay) {
        overlay = new BrowserWindow({
            x: b.x, y: b.y, width: b.width, height: b.height,
            frame: false, transparent: true, backgroundColor: "#00000000",
            webPreferences: { sandbox: true },
        })
        await overlay.loadURL("data:text/html," + encodeURIComponent(
            "<body style=\"margin:0;background:transparent\"><div style=\"position:absolute;left:40px;top:40px;"
            + "padding:12px;background:rgba(255,255,255,.15);color:#fff;font:16px sans-serif\">UI OVERLAY</div></body>"))
        overlay.show()
        overlay.moveTop()
    }

    const pipe = B + B + "." + B + "pipe" + B + "pace-" + label + "-" + process.pid
    const child = spawn(MPV, [clip, "--wid=" + widOf(win), "--input-ipc-server=" + pipe,
        "--no-config", "--no-audio", "--loop-file=yes", "--vo=gpu-next",
        "--video-sync=display-resample", "--no-terminal", "--osc=no"],
        { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] })
    child.stderr.setEncoding("utf8")
    child.stderr.on("data", d => say("mpv[" + label + "]:", String(d).trim()))
    child.on("exit", (c, sig) => say("mpv[" + label + "] exited", c, sig))
    say("mpv path:", MPV, "exists:", existsSync(MPV), "clip:", existsSync(clip))

    let sock = null
    for (let i = 0; i < 100 && !sock; i++) {
        sock = await new Promise(res => {
            const s = net.connect(pipe)
            s.on("connect", () => res(s)); s.on("error", () => res(null))
        })
        if (!sock) await delay(100)
    }
    if (!sock) { say("NO PIPE for", label); await delay(1500); child.kill(); throw new Error("no ipc pipe " + label) }
    let buf = "", id = 0
    const pending = new Map()
    sock.on("data", c => {
        buf += c.toString("utf8")
        let i
        while ((i = buf.indexOf(NL)) >= 0) {
            const line = buf.slice(0, i); buf = buf.slice(i + 1)
            if (!line.trim()) continue
            try { const m = JSON.parse(line)
                if (m.request_id && pending.has(m.request_id)) { pending.get(m.request_id)(m.data); pending.delete(m.request_id) }
            } catch {}
        }
    })
    const get = name => new Promise(res => {
        const rid = ++id
        pending.set(rid, res)
        sock.write(JSON.stringify({ command: ["get_property", name], request_id: rid }) + NL)
    })

    await delay(9000)
    const out = {}
    for (const name of ["display-fps", "estimated-display-fps", "display-sync-active", "vsync-ratio",
                        "vsync-jitter", "frame-drop-count", "mistimed-frame-count", "vo-delayed-frame-count"]) {
        out[name] = await get(name)
    }
    sock.destroy(); child.kill(); win.destroy(); if (overlay) overlay.destroy()
    await delay(1500)
    return out
}

app.on("window-all-closed", () => {})
app.whenReady().then(async () => {
  try {
    if (!existsSync(clip)) { say("missing clip", clip); app.exit(1); return }
    const transparentResult = await run("bare", true, false)
    const opaqueResult = await run("overlay", true, true)
    const lines = ["metric".padEnd(26) + "no UI overlay".padEnd(22) + "UI overlay on top"]
    for (const key of Object.keys(transparentResult)) {
        lines.push(key.padEnd(26) + String(transparentResult[key]).padEnd(22) + String(opaqueResult[key]))
    }
    say(lines.join(String.fromCharCode(10)))
    app.exit(0)
  } catch (error) { say("PROBE FAILED:", error.message); app.exit(1) }
})
