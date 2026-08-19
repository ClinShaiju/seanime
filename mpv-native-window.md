# Native mpv window playback (MpvCore level 3)

Branch: `feat/mpv-native-window`. Goal: MpvCore playback that is indistinguishable from standalone mpv —
`video-sync=display-resample`, `interpolation`, real hwdec presentation — by giving mpv a real window with its
own swapchain instead of screen-scraping its frames through Chromium.

## Why the current path can't do it

mpv-prism renders offscreen (libmpv render API → ANGLE/WGL shared D3D11 texture), ships the texture to the
renderer, and the renderer paints it into a `<video>` fed by a track generator. Two hard limits:

- `MpvPrismMain.js` pumps frames on a fixed `40ms` timer (`v=40`, rescheduled as `max(0, 40 - elapsed)`), so
  presentation is capped at **25 fps** regardless of content or monitor.
- `mpv_render_context_report_swap()` fires after that software-paced capture, never at scanout, so mpv's vsync
  estimate is its own pump cadence. Display-sync has no real clock to lock to, and `interpolation` requires an
  active display-sync mode, so both options are inert today.

## Architecture

```
main window (BrowserWindow, transparent)   <- the whole Seanime UI + player controls, taskbar/alt-tab entry
video window (BrowserWindow, transparent, focusable: false, skipTaskbar)   <- mpv.exe --wid=<HWND> renders here
```

Both are top-level and are raised as a pair (`videoWindow.moveTop()` then `uiWindow.moveTop()`) on every
activation, with the video window following the UI window into the tray/taskbar on hide and minimize.

Electron constraints that force this shape (verified against the docs and in the app):

- `transparent` is **construction-only** — the main window has to be created transparent, so the native backend
  is a Denshi setting that takes effect on restart.
- **Do not re-parent the UI window into the video window.** A child is always above its parent on paper, which
  is why it was tried first, but Windows never lists owned windows, so the app vanished from the taskbar and
  alt-tab — and the video window still managed to end up above the UI after enough clicking. The video window
  is instead `focusable: false` + `skipTaskbar`, so nothing can activate it.
- `--wid` pointed at the *main* window does not work: Chromium paints through DirectComposition, so mpv's child
  HWND either covers the UI or detaches (mpv#10189). Hence the separate host window.

**GPU power state is the dominant performance factor on laptops (found 2026-08-19).** mpv presents ~24x/sec
with the GPU idle between frames, which NVIDIA's default "Optimal power" mode reads as a light workload and
answers by dropping the dGPU to ~800 MHz. A shader chain that takes ~12 ms at full clocks then takes ~40 ms and
misses its deadline, producing drops, jitter above 1.0 and a *falling* vsync-ratio while GPU utilisation sits
around 64% - i.e. the classic "there is headroom but it is not being used" signature. mpv-prism never hit this
because its work went through Chromium's continuously-presenting compositor, which never let the GPU idle.

Fix today is a per-application NVIDIA profile (Power management mode = Prefer maximum performance) for the
bundled `mpv.exe`. That is a manual step every laptop dGPU user needs, so it wants automating via NVAPI
(`NvAPI_DRS_CreateProfile` + `PREFERRED_PSTATE = PREFER_MAX`), which needs a native addon and is NVIDIA-only.
**Measure clocks before blaming the renderer, the shader chain or the embedding** - all three were accused and
exonerated first.

Two findings from `tests/mpv-native-spike.mjs` that are not obvious and must not be "cleaned up":

- **The host window has to be transparent too.** With an opaque (Chromium-painted) host, the two surfaces fight:
  mpv's video covered the UI overlay, and once the UI was raised the video went black. A transparent host paints
  nothing, and both compose correctly. mpv clears its own window to black, so nothing shows through.
  (Transparency costs nothing in presentation terms - measured 2026-08-19 via
  `tests/host-transparency-probe.mjs`: transparent host, opaque host, and transparent host with a UI window
  composited on top all held jitter under 0.001 with zero dropped frames.)
- **`uiWindow.moveTop()` is required after mpv starts.** Creating mpv's child HWND leaves the host above the UI
  window despite the owner relationship, so the overlay is invisible until the UI window is raised once.

mpv runs as a **separate process** driven over `--input-ipc-server=\\.\pipe\seanime-mpv-<id>` (JSON IPC):
no native addon, no build toolchain, and an mpv crash cannot take down Denshi. Audio goes straight to WASAPI,
which makes the EAC3/DTS/TrueHD chromium-routing workarounds irrelevant on this path.

## Contract preserved

`mpv-core.tsx` / `mpv-core-player-inner.tsx` are ~2300 lines written against mpv-prism. The native backend
reimplements the same surface so those files change only at the import site:

- player methods: `load play pause setPaused stop seek setSpeed setVolume setMute selectTrack setProperty
  getProperty observeProperty command runCommand getTracks enterPip exitPip awaitPresentationReady destroy`
- events: `property position duration paused speed volume mute tracks cache state pip ended error`
- component: `MpvPrismVideo` → `MpvNativeVideo` (same children/overlay contract, but renders a transparent
  hole and reports its rect so the video window can be positioned under it)

Raw mpv events are forwarded from main untouched; the typed events are derived renderer-side, mirroring how
prism splits bridge vs. player.

## Stages

- **S1 — main process backend. DONE.** `src/main/mpv-native/`: pipe IPC client, process lifecycle, video
  window + re-parenting, `ipcMain` surface, event forwarding.
- **S2 — renderer backend. DONE.** `MpvNativePlayer` (prism-shaped, checked against `MpvPlayerApi`),
  `useMpvPlayer`, `MpvNativeVideo`, backend selector frozen per session.
- **S3 — transparency + geometry. DONE for the fullscreen player.** Main window created transparent when the
  setting is on, video rect sampled per frame and pushed to main, window move/resize/fullscreen re-sync.
- **S4 — parity. PARTIAL.** Screenshots go through mpv's `screenshot-to-file`; PiP is hidden on this backend;
  the mini player keeps the mpv window hidden. Tracks/subtitles/shaders/chapters ride the existing property
  and command paths but are **not live-tested yet**.
- **S5 — smoothness. DONE, verified in the real app 2026-08-19.** `video-sync=display-resample` plus
  `tscale=oversample`; `interpolation` is now decided per file from the display/video FPS ratio (see below).
  `tests/mpv-native-session.mjs` confirms display-sync engages (144 Hz monitor, 24 fps clip, vsync-ratio 6),
  and a real 1080p HEVC + Anime4K mode-aa HQ session now runs 10 minutes with **zero frame drops**,
  7 mistimed and 19 delayed.

  **No `display-fps-override` any more.** The premise below ("mpv cannot query the monitor itself") is wrong:
  mpv reports 143.988 Hz through `--wid`, matching standalone mpv exactly, on a panel Electron rounds to 144.
  Forcing Electron's integer, then "correcting" it from `estimated-display-fps` sampled 6 s in, fed a reading
  taken during shader compilation back in as fact (it landed on 143.796) and wrecked display sync for the
  whole session. Let mpv measure. Non-integer refresh rates are exactly what it is good at.

  **`interpolation` is conditional.** It makes mpv render every vsync instead of every frame - at 4K that is
  ~6x the output-stage work - and buys nothing when the ratio is already near-integer (24 fps on 143.99 Hz is
  5.998, an even 6-vsync cadence). `session.ts` enables it only when the ratio is off-beat, e.g. 24 on 60.
- **S6 — cleanup. NOT STARTED.** Drop mpv-prism + its 115 MB `libmpv-2.dll` on Windows once the path is proven
  in the real app.

## Verified by the harness

`npm run test:mpv-native` (builds main, then drives `dist/main/mpv-native/session.js`): display-sync-active,
video-sync/interpolation applied, mpv's display-fps matching Electron's monitor, property events flowing,
file-loaded delivery, pause, absolute and relative seeks, and UI-window survival across teardown.
`tests/mpv-native-spike.mjs`, `tests/transparency-probe.mjs` and `tests/mpv-seek-probe.mjs` are the
diagnostic harnesses behind the two window findings above (keep them; they answer "why is it built this way").

**Not yet verified:** anything in the real app. Playback of actual library/debrid streams, track and subtitle
switching, shaders, chapters, fullscreen transitions and the re-parented main window all need a Denshi build
with `mpvNativePlayback` enabled.

## Known gaps / decisions

- **Mini player** needs the UI to punch a hole through an opaque page background, which CSS can't do through
  ancestors. Fullscreen/drawer playback lands first; mini player either keeps prism or gets a `clip-path`
  background layer later.
- **PiP** is currently the `<video>` element's; on this path it becomes a small always-on-top mpv window.
- **Cast** is main-process and resolves its own source URL — unaffected.
- **mac/Linux** keep prism until the equivalent window plumbing exists (`NSView*` / X11 `Window`).
- mpv binary: fetched by `seanime-denshi/scripts/fetch-mpv.mjs` into `binaries/mpv/` (already an
  `extraResources` entry). Extraction uses System32 `tar.exe` — a PATH `tar` may be GNU tar, which misreads
  `H:\...` as a remote host.
