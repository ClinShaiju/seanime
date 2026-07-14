# Upstream issue triage

62 open issues on `5rahim/seanime` (64 examined incl. 2 recently-closed), classified against **this** deployment (multi-user Pi server, TorBox debrid primary, browser + Denshi MpvCore + Tenji clients, nakama rooms, local library via magnet-to-library, AniList). Feature-request-only issues for platforms we don't use are dropped.

`likelyInFork`: **yes** = buggy path exists at our HEAD · **fork-fixed** = our commits already address it · **unknown** = needs deeper check.

## Present in fork — worth fixing

| # | Sev | Area | Summary | Status |
|---|-----|------|---------|--------|
| [#866](https://github.com/5rahim/seanime/issues/866) | high | mediastream | Browser direct-play only works for H.264+AAC — HEVC/AV1/Opus/VP9 codec strings malformed (`info.go:398` hex level + literal `O`), always falls back to transcode | **yes, confirmed in code** → findings F13 |
| [#868](https://github.com/5rahim/seanime/issues/868) | med | mediastream | Subtitle tracks "no file has been loaded" on episode reopen — cached container query races the file re-registration (`mediastream.hooks.ts:36`) | **yes, confirmed in code** → findings F14 |
| [#738](https://github.com/5rahim/seanime/issues/738) | med | nakama | Schedule tab reads the peer's own library, not the host's, when connected as a Nakama peer | yes (no `Schedule` handling in `internal/nakama`) |
| [#679](https://github.com/5rahim/seanime/issues/679) | med | autodownloader | Built-in RSS autodownloader uses one feed with a 75-item cap → missed episodes on non-24/7 desktops; should do one request per anime | yes (built-in Go path, separate from our Nyaa addon) |
| [#870](https://github.com/5rahim/seanime/issues/870) | med | Denshi MpvCore | Letterbox blacks visibly lighter than player background (color/gamma) — same subsystem as the WGL fix but a distinct symptom | unknown (needs live check on Windows build; WGL switch may have incidentally fixed) |

## Present in fork — feature gaps (additive, not regressions)

| # | Area | Summary |
|---|------|---------|
| [#227](https://github.com/5rahim/seanime/issues/227) | TorBox | Usenet support for TorBox debrid — no usenet handling anywhere in `internal/` |
| [#378](https://github.com/5rahim/seanime/issues/378) | library | `.strm` file support (storageless remote-URL library) — cites TorBox workflows; overlaps our debrid+magnet-to-library area |
| [#490](https://github.com/5rahim/seanime/issues/490) | videocore | Volume boost (>100%) in the built-in player — no gain-node in `seanime-web`; relevant to low-volume BDRip audio |
| [#692](https://github.com/5rahim/seanime/issues/692) | torrent client | Network-interface binding for the built-in torrent client (VPN leak protection) — hardening for magnet-to-library downloads |
| [#606](https://github.com/5rahim/seanime/issues/606) | settings | Cross-device settings & extensions sync — would help our web/Denshi/Tenji multi-client setup |

## Low / cosmetic

| # | Area | Summary |
|---|------|---------|
| [#664](https://github.com/5rahim/seanime/issues/664) | skip markers | OP/ED skip only matches literal `OP`/`ED` chapter names, not Opening/Ending/Intro/Credits case-insensitive |
| [#476](https://github.com/5rahim/seanime/issues/476) | scanner | AniDB OP1a/OP1b variant OP/EDs not recognized (metadata-sort cosmetic) |
| [#316](https://github.com/5rahim/seanime/issues/316) | library API | Display tags on library items/queries — unclear if tag data already exposed |
| [#823](https://github.com/5rahim/seanime/issues/823) | picker UX | Skip file-selection screen when a torrent/batch has only one file |
| [#820](https://github.com/5rahim/seanime/issues/820) | source select | Automatic source doesn't fall back to torrent stream when local library lacks the next episode |

## Fork-fixed (already handled — verify only)

| # | Area | Summary | Our fix |
|---|------|---------|---------|
| [#814](https://github.com/5rahim/seanime/issues/814) | videocore | Event dispatch dies permanently after a client disconnects mid-playback (orphaned `ClientId`) | `videocore.go:932-944` (`// #814:` gate + `rebindClient`), commit `e6dbfaaf` — worth a live check on our multi-user setup |
| [#826](https://github.com/5rahim/seanime/issues/826) | web | My List scroll restore | `5d160f5d` |
| [#648](https://github.com/5rahim/seanime/issues/648) | web | Sort applies while searching | `5d160f5d` |
| [#816](https://github.com/5rahim/seanime/issues/816) | web | Trailer as anime-page banner | `a2a5d572` + `1b94873d` |

**Note:** #866/#868 are upstream bugs in code paths our fork inherits unchanged. #866 is the higher-value fix (any non-H.264 local file silently transcodes instead of direct-playing). Neither is on our primary debrid path, so both are medium for this deployment despite being real correctness bugs.
