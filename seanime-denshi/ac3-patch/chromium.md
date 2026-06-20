# Building a custom Electron with AC3 / E-AC3 (and HEVC) for Seanime Denshi (Windows)

Stock Electron ships an FFmpeg/Chromium without the Dolby AC-3 / E-AC-3 decoders, so
Denshi's built-in player (which raw-streams the file to Chromium via `directstream`)
plays the video but gets **no audio** on AC3/EAC3 tracks. This is GitHub issue #508.

There is **no prebuilt Electron** anywhere with AC3/EAC3 audio (castLabs ECS and
AAAhs/electron-hevc were both checked — HEVC only, no Dolby). The codecs sit behind a
**compile-time** Chromium build flag (`enable_platform_ac3_eac3_audio`), so the only fix
is to build Electron from source with that flag on, then swap the runtime into the
installed Denshi.

> The Denshi build already ships HEVC (verified: `canPlayType('hev1…') === "probably"`),
> so its Electron is already a custom from-source build — it's just missing the Dolby flag.
> This guide adds it and keeps HEVC.

---

## 0. Target facts (this machine)

| Thing | Value |
|---|---|
| Denshi install | `C:\Program Files\Seanime Denshi` |
| Main binary | `Seanime Denshi.exe` (renamed `electron.exe`, ffmpeg is statically linked) |
| Electron version | **v39.2.7** (from `seanime-denshi/package.json`; Chromium ≈ 142) |
| App files to preserve | `resources\app.asar`, `resources\app-update.yml`, `resources\binaries\`, `resources\elevate.exe`, `Uninstall Seanime Denshi.exe` |
| Build drive | **`H:\`** (≈ 468 GB free — needs ~120–180 GB) |
| CPU / RAM | Ryzen 7 7840HS 8c/16t / 31 GB (tight but OK with `symbol_level=0`) |

**Time budget:** toolchain ~1–2 h, `gclient sync` ~1–3 h, first `ninja` build **~6–12 h**
on this mobile CPU. Incremental rebuilds are minutes.

---

## Execution ownership (elevated Claude session)

This guide assumes **Claude Code was launched from an elevated terminal**, so every
command Claude runs inherits admin. Under that assumption Claude executes nearly the whole
runbook unattended. What still needs **you**:

| Needs you | Why |
|---|---|
| Launch Claude elevated (one UAC click) | grants admin to all of Claude's shells |
| **Reboot** after the long-paths registry change (§1) | the setting only applies to processes started after reboot; rebooting ends the session → relaunch Claude elevated and say "continue" |
| Keep the session + machine awake during the 6–12 h build | Claude's background build dies if the CLI/session closes; keep it plugged in, sleep disabled |
| Final real playback test | the codec probe proves support; you confirm by actually playing an AC3/EAC3 file |

Everything else below — VS/SDK install, registry, Defender, source sync, build, packaging,
verification, and the `C:\Program Files` swap — Claude runs itself.

> **Guard note (applies even with admin):** Claude Code blocks `Remove-Item` on `C:\Program…`
> paths as a safety layer above Windows ACLs. The swap (§7) therefore *renames* the old
> install to `.backup` instead of deleting it, and keeps any `Remove-Item` and the literal
> `C:\Program` in separate commands so the guard doesn't trip.

---

## 1. Install the build toolchain — Claude runs this (elevated)

### 1a. Visual Studio 2022 Build Tools (scripted, silent)
Chromium refuses to build without C++ tools, the Windows 11 SDK, ATL/MFC, and the
**Debugging Tools for Windows**. Install them unattended:

```powershell
# VS 2022 Build Tools + required C++ components + Windows 11 SDK
winget install --id Microsoft.VisualStudio.2022.BuildTools -e `
  --accept-package-agreements --accept-source-agreements `
  --override "--quiet --wait --norestart `
    --add Microsoft.VisualStudio.Workload.VCTools `
    --add Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
    --add Microsoft.VisualStudio.Component.VC.ATL `
    --add Microsoft.VisualStudio.Component.VC.ATLMFC `
    --add Microsoft.VisualStudio.Component.Windows11SDK.26100 `
    --includeRecommended"

# Debugging Tools for Windows (a Windows SDK feature, installed separately).
# Grab the current Windows 11 SDK installer, then add only the debuggers feature:
$sdk = "$env:TEMP\winsdksetup.exe"
Invoke-WebRequest "https://go.microsoft.com/fwlink/?linkid=2324626" -OutFile $sdk  # current Win11 SDK; update link if it 404s
Start-Process $sdk -ArgumentList "/features OptionId.WindowsDesktopDebuggers /quiet /norestart" -Wait
```

Manual equivalent (if you'd rather use the VS Installer GUI): workload **Desktop
development with C++**, plus **Windows 11 SDK 10.0.26100**, **C++ ATL**, **C++ MFC**, and
**Debugging Tools for Windows** (`Settings → Apps → Windows Software Development Kit →
Modify → Debugging Tools for Windows`).

### 1b. Other prerequisites
- **Node.js** LTS (already present: `C:\Program Files\nodejs`).
- **Git** (already present). Configure it for Chromium's long paths:
  ```powershell
  git config --global core.longpaths true
  git config --global core.autocrlf false
  git config --global core.fscache true
  git config --global branch.autosetuprebase always
  ```
- **Enable Windows long paths** (admin, one-time, then reboot):
  ```powershell
  Set-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' `
    -Name 'LongPathsEnabled' -Value 1 -Type DWord
  ```

### 1c. depot_tools
```powershell
cd H:\
git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git H:\depot_tools
```
Set environment for the build (per-session, or add to System env vars):
```powershell
$env:Path = "H:\depot_tools;$env:Path"     # depot_tools MUST be first on PATH
$env:DEPOT_TOOLS_WIN_TOOLCHAIN = "0"        # use your locally installed VS, not Google's internal one
$env:GYP_MSVS_VERSION = "2022"
$env:vs2022_install = "C:\Program Files\Microsoft Visual Studio\2022\Community"  # adjust edition
```

### 1d. Add a Defender exclusion for the build dir (admin — roughly halves sync/build time)
```powershell
Add-MpPreference -ExclusionPath "H:\electron-gn"
Add-MpPreference -ExclusionPath "H:\depot_tools"
```

---

## 2. Fetch the Electron source (≈ 100 GB, 1–3 h)

```powershell
mkdir H:\electron-gn
cd H:\electron-gn
$env:DEPOT_TOOLS_WIN_TOOLCHAIN = "0"

gclient config --name "src/electron" --unmanaged https://github.com/electron/electron@v39.2.7
gclient sync --with_branch_heads --with_tags
```

`gclient sync` downloads Chromium + Electron pinned to the v39.2.7 DEPS. Re-run it if it
fails partway — it resumes. Result tree: `H:\electron-gn\src`.

---

## 3. Configure the build (GN args)

Electron's `release.gn` already sets `proprietary_codecs = true` and
`ffmpeg_branding = "Chrome"`. You only add the codec flags.

```powershell
cd H:\electron-gn\src

gn gen out/Release --args="import(\`"//electron/build/args/release.gn\`") enable_platform_ac3_eac3_audio=true enable_platform_hevc=true enable_hevc_parser_and_hw_decoder=true symbol_level=0 blink_symbol_level=0"
```

What each flag does:
- `enable_platform_ac3_eac3_audio=true` — **the fix.** Enables AC-3/E-AC-3 demux + allowlist + OS (Media Foundation) decode on Windows.
- `enable_platform_hevc=true` / `enable_hevc_parser_and_hw_decoder=true` — keep HEVC that Denshi already had.
- `symbol_level=0 blink_symbol_level=0` — drops debug symbols: **much** less RAM during link (important at 31 GB) and ~tens of GB less disk.

**Verify the flag exists before a long build** (it lives in `media/media_options.gni`):
```powershell
Select-String -Path media\media_options.gni -Pattern "enable_platform_ac3_eac3_audio"
gn args out/Release --list --short | Select-String "ac3_eac3|platform_hevc"
```
If `enable_platform_ac3_eac3_audio` is **not** found (older branch), use the patch
fallback in the Appendix instead, then re-run `gn gen`.

---

## 4. Build (the long step)

```powershell
cd H:\electron-gn\src
ninja -C out/Release electron
```
Pinned at 100% across 16 threads for hours. If the **final link** OOMs (31 GB is tight),
cap link parallelism:
```powershell
ninja -C out/Release electron -j 14
# or reduce just the heavy link step concurrency:
# ninja -C out/Release electron -d keeprsp
```

---

## 5. Package the runtime into a dist zip

```powershell
ninja -C out/Release electron:electron_dist_zip
# Output: H:\electron-gn\src\out\Release\dist.zip
```
`dist.zip` is a complete Electron runtime: `electron.exe`, all `*.dll`/`*.pak`/`*.bin`,
`icudtl.dat`, `locales\`, `resources\` (with `default_app.asar`), `snapshot_blob.bin`, etc.

---

## 6. Verify the codecs BEFORE touching the install

Extract dist.zip and run the codec probe with the freshly built `electron.exe`:

```powershell
$work = "H:\electron-gn\verify"
Expand-Archive H:\electron-gn\src\out\Release\dist.zip "$work\dist" -Force
New-Item -ItemType Directory -Force "$work\app" | Out-Null

@'
{ "name": "codec-probe", "version": "1.0.0", "main": "main.js" }
'@ | Out-File "$work\app\package.json" -Encoding ascii

@'
const { app, BrowserWindow } = require("electron")
app.commandLine.appendSwitch("no-sandbox")
app.whenReady().then(async () => {
    const win = new BrowserWindow({ show: false, webPreferences: { offscreen: true } })
    await win.loadURL("data:text/html,<html><body>probe</body></html>")
    const r = await win.webContents.executeJavaScript(`(() => {
        const v = document.createElement("video")
        return {
            eac3: v.canPlayType('audio/mp4; codecs="ec-3"'),
            ac3:  v.canPlayType('audio/mp4; codecs="ac-3"'),
            aac:  v.canPlayType('audio/mp4; codecs="mp4a.40.2"'),
            hevc: v.canPlayType('video/mp4; codecs="hev1.1.6.L120.90"')
        }
    })()`)
    console.log("CODEC_PROBE_RESULT " + JSON.stringify(r))
    app.exit(0)
})
'@ | Out-File "$work\app\main.js" -Encoding ascii

$out = & "$work\dist\electron.exe" "$work\app" 2>&1 | Out-String
($out -split "`n") | Where-Object { $_ -match "CODEC_PROBE_RESULT" }
```

**Success looks like:** `{"eac3":"probably","ac3":"probably","aac":"probably","hevc":"probably"}`
(empty `""` for eac3/ac3 means the flag didn't take — do not proceed; revisit step 3).

---

## 7. Swap the runtime into Denshi — Claude runs this (elevated)

> Claude first quits Denshi (`Stop-Process`) and confirms no `Seanime Denshi` or
> `seanime-server-windows` processes remain — files are locked while it runs.

This assembles the new runtime (new Electron + Denshi's own app files) into a staging dir,
backs up the current install by **renaming** (not deleting — see the guard note), then
swaps. Because of the guard, run the `C:\Program` rename/copy separately from any
`Remove-Item`.

```powershell
$install = "C:\Program Files\Seanime Denshi"
$dist    = "H:\electron-gn\verify\dist"          # extracted in step 6
$stage   = "H:\electron-gn\staging"
$backup  = "C:\Program Files\Seanime Denshi.backup"

# 0) make sure it's not running
Get-Process | Where-Object { $_.ProcessName -match "Seanime Denshi|seanime-server" } | Stop-Process -Force -ErrorAction SilentlyContinue

# 1) stage = fresh Electron runtime
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
Copy-Item $dist $stage -Recurse -Force

# 2) drop Electron's placeholder app, bring over Denshi's real app files
Remove-Item "$stage\resources\default_app.asar" -Force -ErrorAction SilentlyContinue
Copy-Item "$install\resources\app.asar"       "$stage\resources\" -Force
Copy-Item "$install\resources\app-update.yml" "$stage\resources\" -Force -ErrorAction SilentlyContinue
Copy-Item "$install\resources\elevate.exe"    "$stage\resources\" -Force -ErrorAction SilentlyContinue
Copy-Item "$install\resources\binaries"       "$stage\resources\binaries" -Recurse -Force -ErrorAction SilentlyContinue

# 3) rename Electron's exe to Denshi's name + keep the uninstaller
Rename-Item "$stage\electron.exe" "Seanime Denshi.exe"
Copy-Item "$install\Uninstall Seanime Denshi.exe" "$stage\" -Force -ErrorAction SilentlyContinue

# 4) back up current install, then replace
if (Test-Path $backup) { Remove-Item $backup -Recurse -Force }
Rename-Item $install $backup
Copy-Item $stage $install -Recurse -Force

Write-Output "Done. Launch Denshi and play an AC3/EAC3 file."
```

If anything is wrong, roll back: delete `C:\Program Files\Seanime Denshi` and rename
`Seanime Denshi.backup` back to `Seanime Denshi`.

---

## 8. Caveats — read before relying on this

- **Auto-update overwrites it.** Denshi uses `electron-updater`; the next app update
  replaces the custom runtime with the official (unpatched) one, and AC3/EAC3 breaks
  again. Mitigations: disable auto-update / pin the version, or have Claude re-run §7
  after an update (only while an elevated session is alive — otherwise it's a manual
  re-apply). Keep the built `dist.zip` and staging dir so re-applying is just the §7 copy.
- **Electron version must match the app.** Build exactly **v39.2.7** so `app.asar`
  (compiled against that Electron) runs cleanly. A different minor (e.g. ECS's 39.8.10)
  usually works but isn't guaranteed.
- **Code signature is voided.** Replacing the exe drops Denshi's signature; Windows
  SmartScreen may warn on first launch. It still runs.
- **Licensing.** On Windows, `enable_platform_ac3_eac3_audio` decodes via the OS Media
  Foundation Dolby decoder (no bundled decoder) — low exposure. If you ever build for
  Linux, there's no system Dolby decoder and you fall back to FFmpeg software decode,
  which carries Dolby patent-licensing obligations if redistributed.
- **This fixes only this machine.** Other clients (and the web player) still can't decode
  AC3/EAC3. The cross-client fix is server-side audio transcode in `directstream`.

---

## Appendix A — Patch fallback (if the GN flag isn't enough)

If `enable_platform_ac3_eac3_audio` is missing on the v39 branch, or AC3 still routes to
nothing, apply the maintainer's own codec patches and rebuild. They're pinned to
v36.2.1 and must be forward-ported to v39.2.7 (Chromium's media/ffmpeg wiring shifts
between versions).

- `5rahim/electron-media-patch` — HEVC + AC3 + E-AC3 (the recipe the Denshi build derives from)
- `ThaUnknown/electron-chromium-codecs` — same family of patches, more history

The three patches apply to different trees, from `H:\electron-gn\src`:
```powershell
git -C third_party/ffmpeg apply  <path>\look_ffmpeg_hevc_ac3.patch
git apply                         <path>\look_chromium_hevc_ac3.patch     # in src/
git -C electron apply             <path>\look_electron_hevc_ac3.patch
```
Resolve any reject (`.rej`) hunks by hand against the v39 source, then re-run
`gn gen` (step 3) and `ninja` (step 4).

## Appendix B — Troubleshooting

| Symptom | Fix |
|---|---|
| `gclient sync` fails on long paths | confirm `core.longpaths true` + `LongPathsEnabled=1` + reboot |
| `gn gen` "Unknown variable enable_platform_ac3_eac3_audio" | flag absent on branch → use Appendix A |
| Link step OOM / swap thrash | keep `symbol_level=0`; lower `ninja -j`; close other apps |
| `vs2022_install` not found | point it at your exact VS edition path (Community/Professional) |
| Probe shows `eac3:""` after build | flag didn't compile in; re-check `gn args out/Release --list` |
| Denshi shows blank window after swap | Electron/app.asar version mismatch — rebuild exactly v39.2.7 |
| AC3 works, EAC3 doesn't (or vice-versa) | both come from the same flag; if split, you're on the FFmpeg-decode path (Appendix A) — check the ffmpeg patch's codec list |

---

### TL;DR (elevated Claude session)
Claude runs 1–6 and the §7 swap itself. **You** only: launch Claude elevated, reboot once
after the long-paths reg change, keep the session/machine awake through the build, and do
the final playback test.
1. Install VS 2022 (+SDK +Debugging Tools) via winget, depot_tools, enable long paths → **reboot (you)**.
2. `gclient sync` Electron **v39.2.7** into `H:\electron-gn`.
3. `gn gen` with `enable_platform_ac3_eac3_audio=true enable_platform_hevc=true enable_hevc_parser_and_hw_decoder=true symbol_level=0`.
4. `ninja -C out/Release electron` then `electron:electron_dist_zip`.
5. Probe the new `electron.exe` → expect `eac3:"probably"`.
6. Swap into `C:\Program Files\Seanime Denshi`, preserving `resources\app.asar` + `binaries\`, rename exe (rename old → `.backup`).
7. Re-apply §7 after every Denshi auto-update.
