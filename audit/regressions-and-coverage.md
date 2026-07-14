# Regressions, by-design conflicts, and coverage

## Regressions the fork already repaired (no action needed)

These were real defects at some commit but a **later fork commit fixed them** — listed so a future "did X regress?" question resolves fast. Verified fixed at HEAD.

| Area | Defect (when introduced) | Fixed by |
|------|--------------------------|----------|
| nakama | `broadcastRoomState` marshaled the live `*WatchRoom` (live Participants map) outside the lock — data race (`b107c559`) | `e19c8845` → `snapshotLocked()` (`watch_room.go:1016-1032`) |
| profile | New unlinked non-admin session fell back to the **admin's** simulated/local collection (`526843bb`) | `2a844337` |
| profile | Global event plane claimed the admin's real id on networked servers, ~1 day (`98e9d742`) | `52fcf6c4` (`adminEventsUserID = systemUserID` when password set) |
| profile | `dataUserID` fell back to admin for **any** unresolved identity, not just password-less installs (`a89fd47a`) | `e9be7e61` (anon-hardening unconditional) |
| debrid | `dd6e63a0` claimed per-user streaming but core StreamManager stayed a single shared instance | `9b969cc3` (per-user managers) |
| debrid | `HandleDebridCancelStream` resolved the wrong user's directstream via shared state (`dd6e63a0`) | `9b969cc3` (Options.UserID scoping) — but see F9: a related nil-prevOpts path may still fall through |
| directstream | v3.8.10 always-on read-ahead broke debrid playback (`57187933`) | reverted `3fb8e616`, re-added opt-in default-off `77b57779` (v3.8.11) |
| debrid/prewarm | `PrewarmStreams` fan-out ran resolves concurrently despite staggered kickoffs; probe treated CDN 429 as dead-link | `db1a4b59` |
| profile | `HandleUserLogin` had no brute-force throttle; `HandleUserRegister` took an unvalidated role string (`96f8ab41`) | `8760d144` |
| debrid | Preload added concurrent writers to StreamManager fields with no mutex (`37d5a2ab`/`c38ae2bd`) | later mutex work; race-tested clean `87385960` |
| web | Banner trailer shipped with no toggle + white-fade embed (`a2a5d572`) | `1b94873d` |
| build | `a00983fe`/`166e2960` rewrote files as CRLF (obscured diffs) | `01400140` (normalize to LF) |

## By-design choices flagged for awareness

- **F-INV (cache dominates format quality within a band)** — see findings.md. The one item where the fork's deliberate design may conflict with your stated quality-over-cache invariant. Needs your adjudication.
- **F16 (admin-only schedule/missing/upcoming caches)** — deliberate, documented in-code to avoid cross-user leakage; only the schedule path has real remote-call waste for non-admins.
- **`2b602d6f` UserOnly gate** distinguishes anon vs logged-in but **not** user vs. other-user for shared debrid torrent management (`HandleDebridCancelDownload/DeleteTorrent/GetTorrents`, `debrid.go:247-330`). On a shared TorBox account this is arguably intended (one shared queue), but any logged-in user can cancel/delete another user's debrid download. Confirm this is acceptable for your trust model.

## Unclear / low-confidence (worth a glance, not urgent)

- `aa346c98` mutates the shared `*hibiketorrent.AnimeTorrent.InfoHash` in place during file selection — possible aliasing if the same pointer is reused across candidates.
- Preview-list rank map keyed by `InfoHash`, which hash-less aggregator results fill with the raw torrent **Name** — collision risk if two releases share a name.
- `UpsertTheme` per-user row resolution is check-then-act with no unique-constraint backstop (a regression from an atomic `ON CONFLICT` upsert) — races on concurrent theme writes for one user.
- `getTorrentsCached` returns the cached slice by reference — possible aliasing with a slice still being appended elsewhere.
- In-place merged-season playback always prefers debrid auto-select over an existing **local** library file (`merged-season-section.tsx`) — may be intended, but wastes a debrid resolve when the file is already local.

## Coverage

**What was examined:** all 179 fork-only non-merge commits (12 batches), the manual conflict-resolutions of all 3 upstream merges (`--cc` combined diffs), 7 subsystems read at HEAD (debrid, autoselect, directstream/mediacore/videocore, profile/core/handlers, nakama, web, continuity/franchise), and 62 open upstream issues. `origin/next` fully merged — no missing upstream fixes.

**Fork features verified intact at HEAD** (counted, not defective): next-episode preload chaining; prewarm watched-metadata/airing buckets; truthful live-check prewarm badge + invalidation + cold-resolve fallback + n-2 cleanup + UserTags refcount; rate-limit safety (paced requestdl, serialized prewarm, 429 tagging); seamless reconnect + 2h URL refresh; per-user StreamManager/prewarm isolation; per-link CDN gate + non-2xx rejection; chunked/cached metadata reads; CDN 429/5xx retry+backoff; read-ahead default-off; per-session mediacore Coordinator (57ad877d still wired); videocore rebind-to-live-client (#814); season grouping (merged-season, mislabeled-sibling separation, subtitled-sibling retention, relation-aware extras); durable `_lw` sort reads the durable store; multi-season pack `seasonCovered` + dash-season parsing; profile auth middleware, per-user settings/theme/playlists/AniList cache isolation, anon browse-only.

**Limits of this audit:**
- Static only — no runtime/live testing. Perf findings (M5c, M6c) are code-traced, not profiled.
- Two adversarial-verify passes ran (the first lost the nakama-rooms commit batch to an infra error; the second re-ran it and surfaced C1/C4/M4a/M5a/M5b/M7a). A transient API outage killed 12 verifier calls in pass 2 — the 6 code-correctness ones among them are now all verified (4 by hand: C2/H1/M3a/M3b; 2 via sibling verifiers: M6a/M6c). The 6 remaining were upstream issue-triage grep-checks. Only **M2c** stays PLAUSIBLE (traced, not independently re-verified).
- The security criticals (C1–C3) and their linchpin `isTrustedRequest` were re-verified by hand against the route table and guard chain.
- Generated files (`api/generated/`, `codegen/generated/`), CHANGELOGs, and version bumps were skipped by design.
