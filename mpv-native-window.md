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
video window (BrowserWindow, opaque, parent)   <- mpv.exe --wid=<HWND> renders here
   └── main window (BrowserWindow, transparent, child)  <- the whole Seanime UI + player controls
```

Electron constraints that force this exact shape (verified against the docs):

- `transparent` is **construction-only** — the main window has to be created transparent, so the native backend
  is a Denshi setting that takes effect on restart.
- A child window is **always above its parent** — therefore the video window is the parent and the main window
  is re-parented into it (`mainWindow.setParentWindow(videoWindow)`) while a native player exists.
- `--wid` pointed at the *main* window does not work: Chromium paints through DirectComposition, so mpv's child
  HWND either covers the UI or detaches (mpv#10189). Hence the separate host window.

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

- **S1 — main process backend.** `src/main/mpv-native/`: pipe IPC client, process lifecycle, video window +
  re-parenting, `ipcMain` surface, event forwarding. *(this session)*
- **S2 — renderer backend.** `MpvNativePlayer` (prism-shaped), `useMpvNativePlayer`, `MpvNativeVideo`, and a
  backend selector so Windows + setting → native, everything else → prism.
- **S3 — transparency + geometry.** Main window created transparent when enabled, video rect → screen bounds
  sync (ResizeObserver → IPC), fullscreen/drawer path first.
- **S4 — parity.** Tracks, external subtitle files, Anime4K shaders, screenshots (mpv `screenshot-to-file`),
  chapters/skip, stats (now including `display-sync-active`, `vsync-ratio`, `mistimed-frame-count`).
- **S5 — smoothness.** `video-sync=display-resample`, `interpolation=yes`, `tscale`, user mpv.conf passthrough,
  then verify display-sync actually engages.
- **S6 — cleanup.** Drop mpv-prism + its 115 MB `libmpv-2.dll` on Windows once the native path is proven.

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
