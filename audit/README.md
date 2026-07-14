# Seanime fork audit — 2026-07-09

Full audit of the `ClinShaiju/seanime` fork against upstream `5rahim/seanime`.

- **Fork point:** upstream `315c8c9e` (2026-06-19)
- **HEAD audited:** `fd2ffc25` (v3.9.4, 2026-07-09) — 179 fork-only non-merge commits + 3 upstream merges
- **Upstream merges reviewed:** `20596e8b` (v3.9.0 "Kizami"), `b7406f2b` (next 3.9.1-alpha.3), `ce4181ac` (v3.9.1 nakama/mpvcore fixes)
- **Method:** 24 disjoint-scope sweep agents (2 issue pages, 12 commit batches, 3 merge-resolution diffs, 7 subsystem sweeps) → dedup → one adversarial refuter per live finding. `origin/next` is fully merged; no upstream fixes are missing from the fork.

## Reports

| File | Contents |
|------|----------|
| [findings.md](findings.md) | Ranked live findings (confirmed + plausible), with fix pointers |
| [upstream-issues.md](upstream-issues.md) | 62 open upstream issues triaged against this deployment |
| [regressions-and-coverage.md](regressions-and-coverage.md) | Fork-repaired regressions, by-design conflicts, per-subsystem coverage |

## TL;DR — act on these first

**One systemic root cause dominates the security findings:** `isTrustedRequest` returns `true` whenever a server password is set (`local_security.go:279`). On a password-protected multi-user server — this deployment — every "privileged" guard built on it is a no-op, so *any password holder* (regular user or browse-only anon) inherits admin power. Verified by hand.

1. **CRITICAL ×4** (findings C1–C4):
   - **C1 — Extension install/uninstall reachable by any password holder** → remote code into the shared plugin sandbox affecting all users.
   - **C2 — Server self-update / download-release reachable by any password holder** → binary swap + restart / forced downgrade / restart-loop DoS.
   - **C3 — All server secrets leak** via `GET /settings`, `/status`, `/debrid/settings`: TorBox API key, **the server/Nakama password itself**, qBittorrent/Transmission/VLC passwords, translate API key — unredacted to non-admin/anon.
   - **C4 — Server-crash panic**: nil `playbackCtx` → `context.WithCancel(nil)` in the unrecovered subtitle-kick goroutine, during episode-switch-with-seek.
2. **HIGH ×2**: watch-room sync is **dead on MpvCore** (Denshi's default player since v3.9 → the feature is non-functional for the primary client, H1); browser direct-play broken for all non-H.264 files (upstream #866, H2).
3. **Quality-vs-cache invariant (F-INV)** — the fork made cached-first **dominate format quality within an audio band** (test-pinned "cached 480p beats uncached 1080p"). Conflicts with your decree that cached-first must be a tie-break. Needs your adjudication.
4. ~14 medium bugs: multi-user isolation gaps, MpvCore/VideoCore atom-split regressions (auto-resume, nakama coordinator), directstream slot leaks, session goroutine leaks, continuity disk-I/O. See findings.md.

_No code was changed by this audit._
