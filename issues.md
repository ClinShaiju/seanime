# Seanime — Issue Notes (filtered: Windows · Denshi · debrid)

From [5rahim/seanime](https://github.com/5rahim/seanime), trimmed to my setup.
**Dropped:** Linux/MPRIS, online-streaming-scraper issues, torrent-streaming-only bugs, FLAC audio (#608),
Firefox-only (#832, I'm on Denshi/Chromium), AC3/EAC3 (#508, patched locally), plugin-dev APIs,
Auto Downloader / RSS (torrent), Nakama, Windows ARM64 (#621). Manga kept — cut it if I don't read manga here.

---

## 🐛 Bug Fixes

1. **#814 — videocore stops dispatching events after a mid-playback disconnect** *(open)*
   Orphaned `currentState.ClientId` kills **all** built-in-player events (subs, progress, skip) until server restart.
   Hits debrid playback. Well-diagnosed.

2. **#709 — Infinite React loop when Auto-Select is off (debrid batches)** *(open)*
   Batch debrid only loads ep 1 / loops. Directly relevant — keep Auto-Select on as a workaround.

3. **#476 — Openings/Endings with variants not recognized** *(open)*
   Breaks auto-skip OP/ED.

4. **#663 — Discord Rich Presence not firing with external player / direct play** *(open)*

---

## ✨ QoL Improvements

1. **#838 + #823 — One-click streaming** *(open)*
   Pre-select the "Likely Match" + Enter, and skip the confirm screen when there's a single file. Applies to the
   debrid file picker — fewer clicks every stream.

2. **#826 — Remember My List scroll position on back-navigation** *(open)*
3. **#801 — Surface the keyboard-shortcuts list (settings/docs)** *(open)*
4. **#819 — Show title/episode on hover without pausing** *(open)*
5. **#829 / #763 — Screenshot save without a prompt / clipboard-only mode** *(open)*
6. **#648 — Apply sort order while searching** *(open)*
7. **#619 — Sort Planning list by Last Updated / Date Added** *(open)*
8. **#776 — Per-anime "Hide spoilers" override** *(open)*
9. **#687 — Playback controls in the built-in player's PiP** *(open)*
10. **#490 — Volume boost in the built-in player** *(open)*
11. **#675 — Alt + wheel horizontal scroll on Home/Discover** *(open, low)*
12. **#818 — Resizable mini player that remembers size** *(open, low)*
13. **#779 — Locked / non-resizable fullscreen (Windows kiosk)** *(open, low)*

---

## 🚀 Feature Additions

1. **#769 — More Debrid services** *(open, accepting PRs)*
   RD outages broke >50% of titles; reduce single-provider risk. Closed-but-wanted debrid options worth pushing:
   **#331 StremThru multi-debrid**, **#310 Premiumize**, **#227 Usenet / TorBox**.

2. **#667 — Base URL / reverse-proxy support** *(open)* — remote self-host access; contributor already offered.
3. **#757 — Raise the 20-episode playlist cap** *(open)*
4. **#816 — Use the anime trailer as the page banner** *(open)* — AniList already exposes `trailer`.
5. **#652 — Casting (Chromecast/etc.)** *(open, high complexity)*
6. **#809 — Override AniList "aired episodes" count** *(open)* — makes CR-early releases watchable.
7. **#727 / #720 — Edit (or read-only) config from the app** *(open, low)*
8. **#467 — Switch title language without logging into AniList** *(open, low)*

### Manga *(remove if not used)*
- **#840** — multi-provider unread counts, auto-pick & source exclusion
- **#789** — recognize local manga by volume (vol/v1/v_1…)
- **#502** — auto-download chapters on release
- **#660** — chapter upload date

---

## 🔌 Plugins *(only if I use community plugins)*

- **#824** — install extensions from a marketplace (protocol handler / local endpoint)
- **#606** — sync settings & extensions across devices

---

*Numbers are GitHub issue IDs. Verify state before acting — the tracker moves fast.*
