# Seanime Tenji (iOS) — Issue Notes (filtered: iOS · debrid)

From [5rahim/seanime-tenji](https://github.com/5rahim/seanime-tenji), trimmed to my setup.
**Dropped:** Android-specific bits, torrent-only requests (#7), and the on-device server port
([seanime-server-mobile] — I run the server on Windows, not the phone).

---

## 🚀 Feature Additions (open)

1. **#8 — Debrid: download locally without the server hop** *(open)*
   Today the episode must download on the server first, then to the client. For debrid the provider serves it, so the
   server round-trip is pure overhead. Top priority — directly debrid.

2. **#4 — Pick up subtitle files from the same directory** *(open)*
   Like MPV's default (matching basename). Tenji doesn't forward sibling subs, so raws / externally-subbed shows show
   **no subtitles** in the built-in or external player. Also: no way to edit external-player links.

### Manga *(remove if not used)*
- **#6** — manga header: only show titles with unread chapters + refresh-sources button
- **#5** — volume keys to turn manga pages (toggle)

---

## ✅ Closed but relevant

- **#3 — Accept self-signed SSL certs (trust-on-first-use)** — needed to reach my own server over custom TLS;
  confirm it actually shipped.
- **#2** — disable subtitles (None/Off); **#1** — double-tap zoom in reader. Both likely already done.

---

## 🔄 Desktop ↔ iOS Parity (debrid / iOS only)

Verify each against the current Tenji build before acting — hypotheses, not confirmed gaps.

**High**
1. Sibling subtitle files + external-sub support (#4).
2. Direct debrid download, no server round-trip (#8).
3. Audio/subtitle track selector parity — None/Off, per-track switching, delay (web UI has the full selector).
4. OP/ED auto-skip parity — confirm the libmpv player exposes the same skip markers/buttons as desktop videocore.
5. Self-signed / custom-TLS server connections (#3).
6. AirPlay / casting — desktop #652; arguably more natural on iOS, worth its own request.

**Medium**
7. Remember last-used source/provider between sessions.
8. Playlists (incl. desktop's 20-ep cap, #757).
9. Schedule tab parity.
10. Settings / extension sync across devices (desktop #606).

---

*Numbers are issue IDs in seanime-tenji. Parity items are to confirm against the live app.*
