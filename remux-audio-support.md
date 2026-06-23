# Handoff: REMUX + lossless-audio support

## Problem

The debrid **directstream** path is a byte-range passthrough of the original MKV to the client's
player. Audio plays only if the *client* can decode the track's codec:

| Client | Decodes |
|---|---|
| **Denshi** (Electron/Chromium + custom ffmpeg, EAC3 routing patch) | AAC, AC3, EAC3, FLAC, Opus, MP3 |
| **Web browser** (Chromium/Firefox) | AAC, AC3*, EAC3*, FLAC, Opus (no proprietary lossless) |
| **Tenji** (mpv) | everything |

**Not decoded by the Chromium clients:** DTS, DTS-HD, TrueHD, **PCM/LPCM**. These play *silently*.

This is why:
- BluRay **REMUX** releases (which the user wants — anime discs are often the uncensored / re-edited
  cut with restored detail) carry DTS-HD/TrueHD/PCM and so play silently on Denshi/web.
- The autoselect **REMUX bonus is currently disabled** (see `internal/torrents/autoselect/comparison.go`,
  note near `scoreSeadexBest`) to avoid auto-picking silent-audio releases until this is solved.
- Only **FLAC** is rewarded among lossless audio (it decodes everywhere).

Tenji (mpv) already handles all of this — no work needed there.

## Why "just patch ffmpeg" doesn't fix Denshi

Electron loads ffmpeg as a shared lib, but **Chromium gates audio codecs in its own media pipeline**
(`media::IsSupportedAudioType` + the FFmpegDemuxer track allowlist) — independent of what the bundled
ffmpeg can decode. A DTS/TrueHD track isn't even exposed to `<video>.audioTracks`; it's dropped before
ffmpeg is consulted. (This is exactly why the EAC3 work needed *Chromium routing edits*, not just the
published ffmpeg patch — see memory `denshi-eac3-decode-fix`.)

## Options

### A. Server-side selective audio transcode  (universal, but transcode)
On the server, when the selected audio track is a codec the client can't decode, transcode **only the
audio → AAC** and **copy the video** (HEVC passthrough). Audio-only transcode is cheap (~a few % CPU,
fine on the aarch64 VPS; the expensive video encode is avoided).

Building blocks already exist:
- `internal/mediastream/transcoder/audiostream.go` already does `-c:a aac`.
- The HLS path already supports **audio-track switching** (`hlsSetAudioTrack`), so this also closes the
  audio-switch gap (memory `directstream-audio-switch-gap`).

Implementation shape — **codec-aware routing**:
- playable audio (AAC/AC3/EAC3/FLAC) → keep fast directstream passthrough,
- unsupported audio (DTS/TrueHD/PCM) → route through the transcoder.

Main work: the transcoder assumes a **local file path** (`transcoder/stream.go:450 "-i", ts.file.Path`,
plus hash-from-path + keyframe probing). It must accept a **debrid URL** as ffmpeg `-i` (ffmpeg streams
HTTP natively) and probe/seek over HTTP. That URL plumbing + probing is the bulk of the effort.

Works for **all** clients (web, Denshi, Tenji). **User preference: avoid transcode if possible** — so
this is the fallback, not the first choice.

### B. Custom Denshi (Electron/Chromium) build  (no transcode, Denshi-only)
Patch Chromium source — the audio-codec allowlist **and** the demuxer track routing — to admit
DTS/TrueHD/PCM, build ffmpeg with those decoders, and **rebuild Electron**. Heavy (full Chromium
toolchain, multi-hour builds, must be redone on Electron bumps), and it **only fixes Denshi** — web
browsers still can't (you can't patch the user's browser). PCM is the lightest of the three; DTS/TrueHD
are proprietary and the most involved.

### C. Per-client routing  (lazy, no new code on the hot path)
Keep directstream passthrough. For releases with client-unsupported audio, prefer routing playback to an
**external player (mpv)** where available (Tenji already; Denshi can shell to mpv). Selection stays
quality-first; playability handled by player choice. No transcode, no Chromium rebuild — but Denshi/web
in-app playback of those tracks stays silent.

## Recommendation

Given "avoid transcode": there is **no transcode-free way to play DTS/TrueHD/PCM in the Chromium
in-app player** other than the heavy custom-Electron build (B), and even that leaves web broken. If
in-app native playback of REMUX audio on Denshi/web is required → **A** (universal, modest CPU). If
Denshi-only is acceptable and the build cost is worth it → **B**. Otherwise **C** (route those to mpv)
is the zero-transcode compromise.

## Re-enabling the REMUX selection bonus

Once playback is handled (A, B, or C), restore the bonus in
`internal/torrents/autoselect/comparison.go`:
1. Add the const back near `scoreSeadexBest`:
   `scoreRemux = 12 // BluRay REMUX — untouched disc video / uncensored cut`
2. In `calculateScoreBreakdown`, after the SeaDex-best bonus:
   ```go
   if containsBoundedTerm(name, "remux") {
       bonus += scoreRemux
   }
   ```
It's a within-band bonus (like 10bit/HDR/FLAC), so it never overrides the English-dub priority.

## Related

- Memory: `directstream-audio-switch-gap`, `denshi-eac3-decode-fix`, `aiostreams-topology-latency`.
- Task #32 tracks the implementation.
