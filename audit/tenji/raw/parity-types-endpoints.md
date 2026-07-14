# Mechanical drift check: generated API layer (web vs tenji)

Compared:
- `H:/Projects/seanime/seanime-web/src/api/generated/` (HEAD, seanime 3.9.5, commit 5a8a9b74)
- `H:/Projects/seanime-tenji/src/api/generated/` (synced at seanime 3.9.1-alpha.3, tenji commit 2d023d4)

Files in both: `endpoint.types.ts`, `endpoints.ts`, `hooks_template.ts`, `library_explorer.hooks.ts`, `queries_tmpl.ts`, `types.ts`.

## Method
1. Line counts both sides (web 14523 total / tenji 14080 total — close).
2. `endpoints.ts`: diffed `key:` names and `endpoint:` URL strings (full sorted sets, not just line diff).
3. `types.ts`: diffed top-level `export type|interface` names (sorted sets), then a raw unified diff of file content to catch same-name-different-body drift (field adds/removes/enum-member adds within an existing type).
4. `endpoint.types.ts`: raw diff (small file, request/response Variables types).
5. `hooks_template.ts` / `queries_tmpl.ts` / `library_explorer.hooks.ts`: raw diff, then sanity-checked whether differences were real content or just CRLF/LF.
6. Cross-checked backend git log for handler/route changes since 3.9.1-alpha.3 that *should* have produced new endpoints but might not have gone through codegen (hand-authored fetches bypass the generated registry), specifically the artwork-cache endpoint named in the task brief.

## Findings

### 1. `endpoints.ts` (endpoint registry) — only diff is DummyDebrid
```
$ diff <endpoint keys, web> <endpoint keys, tenji>
100d99
< GetDummyDebridSettings
268d266
< SaveDummyDebridSettings
```
```
$ diff <endpoint URLs, web> <endpoint URLs, tenji>
35d34
< endpoint: "/api/v1/debrid/dummy/settings"
```
`GetDummyDebridSettings` (GET) and `SaveDummyDebridSettings` (PATCH) both hit `/api/v1/debrid/dummy/settings`. `Models_DummyDebridSettings` / `Models_DummyDebridFile(s)` are the only missing types in `types.ts` (confirmed: diffing exported type names gives exactly these 3 and nothing else). This is the **mock/test debrid provider** (`internal/database/models/models.go` — fields like `readyDelayMs`, `progressIntervalMs`, `firstByteDelayMs`, `bandwidthBytesPerSecond`, `chunkSize`, `jitterMs`) used for e2e testing debrid streaming without a real provider account. It is gated behind `INTERNAL_FeatureFlags.dummyDebrid` (see below) and is not a real user-facing feature — no settings screen should expose it. **Not a parity gap.**

Every other endpoint (283 in web vs 281 in tenji, diff = exactly the 2 dummy-debrid ones) is present in both, byte-identical URL/method. This includes all the endpoints for features named in the task brief as things to check: magnet-to-library (`TorrentClientAddMagnets`/`TorrentClientAddMagnetFromRule` at `/api/v1/torrent-client/rule-magnet` etc.) — endpoints ARE present in tenji's generated layer identically to web; the magnet-to-library gap (tracked as known-deferred) is a **UI-only** gap, not a codegen/API drift.

### 2. `types.ts` — 4 real diffs beyond DummyDebrid
Ran a full unified diff of the 6339-line (web) vs 6292-line (tenji) file. After removing the ~47-line DummyDebrid block (item 1), exactly 4 substantive hunks remain:

**a) `INTERNAL_FeatureFlags.dummyDebrid: boolean`** (internal/core/feature_flags.go) — new field, paired with item 1's mock provider. Internal-only flag, not surfaced in any user settings UI on web either. Not a gap.

**b) `DebridClient_PrewarmStatusItem` doc-comment only** (internal/debrid/client/prewarm_db.go) — the JSDoc above the type changed wording (old: "will play instantly"; new: "a live check against the parser cache, not the recorded prewarm intent, so the badge can't claim warmth the play won't get") but the **field list is byte-identical** (`mediaId`, `episodeNumber`, `anidbEpisode`, `metadata: boolean`). This documents the semantics change from the 800f59bd "truthful (live-check) metadata badge" stability pass (memory: debrid-preload-prewarm.md), which is on the baseline's "already ported" list (torrent-picker Cached/Uncached + instant-availability badge). Verified tenji already consumes this exact field: `src/components/features/anime/prewarm-badge.tsx:37: const hot = !!match.metadata` and `src/api/hooks/debrid.hooks.ts:148`. Since the wire shape didn't change, no client code needs to change — the live-check truthfulness is entirely a backend behavior change that flows through the same boolean. **No drift, ported item confirmed still correct.**

**c) `MpvCore_ClientEventType` union gained `"startup-timing"`** (internal/mpvcore/types.go) — new event type emitted by MpvCore (mpv-prism/Electron-only native player IPC). Tenji has no MpvCore — it uses `expo-mpv-player` with its own event model (`src/lib/player/use-mpv-player.ts`). **N/A for iOS** per the Electron-shell/MpvCore applicability rule — this is an internal perf-instrumentation event for the desktop native player pipeline, not a UX behavior that needs a phone equivalent (unlike loading-screen text/artwork, which does apply). Confirmed neither web nor any cross-platform code consumes this for user-facing behavior beyond MpvCore-internal timing diagnostics.

**d) `Nakama_WatchPartySessionMediaInfo.media?: AL_BaseAnime`** (internal/nakama/watch_party.go) — new optional field added to the *legacy* single-session watch-party model (used by `seanime-web/src/app/(main)/_features/nakama/nakama-manager.tsx` and the onlinestream page). Checked: **this field is not yet consumed anywhere in seanime-web's frontend** (`grep -rn "currentMediaInfo\?\.media"` → no hits outside generated/types.ts), so it's backend groundwork only, not a shipped web feature tenji is behind on. Also checked which nakama data model tenji's watch-room UI actually uses: tenji's `watch-rooms-sheet.tsx` and `use-watch-room-sync.ts` are built against the **newer** `Nakama_RoomCard` / `Nakama_RoomParticipant` pool-and-multi-room model (matches memory: nakama-rooms-pool.md — "frontend UI remains" was for web, but tenji shipped a room-list UI on this newer model), not `Nakama_WatchPartySession`. So this specific field lives on a model tenji doesn't currently drive its room UI through at all. **Not an actionable drift** — flag as informational only, re-check if/when tenji or web starts consuming `currentMediaInfo.media` (would let a joining client show title/cover art without a separate AniList round trip).

### 3. `endpoint.types.ts` — only the DummyDebrid `Variables` type (consistent with item 1)
```
28d27
<     Models_DummyDebridSettings,
536,546d534
< export type SaveDummyDebridSettings_Variables = { settings: Models_DummyDebridSettings }
```
No other Variables/request-body type drift. Confirms items 1–2 are the complete list of real schema drift.

### 4. `hooks_template.ts`, `queries_tmpl.ts`, `library_explorer.hooks.ts` — not real drift
- `hooks_template.ts`: 3151 (web) vs 2787 (tenji) lines, but **every function is commented out** (`grep -c "^export function use"` on live/uncommented code = 0 both sides). It's pure codegen scaffolding never imported by app code; the size delta is exactly the 2 dummy-debrid endpoints' commented boilerplate (10 `DummyDebrid` references). No functional impact either way.
- `queries_tmpl.ts` and `library_explorer.hooks.ts`: line-by-line diff initially looked like 100% of lines changed, but this is a **CRLF (web) vs LF (tenji) line-ending artifact** (`file` command confirms: web = "CRLF line terminators", tenji = plain ASCII/LF). Content is byte-identical modulo line endings. Not a real difference.

### 5. New backend endpoint NOT in either generated layer — artwork-cache (explicitly asked about in task brief)
`git show --stat 2397ef32` ("feat: cache loading screen artwork server-side, prefetch on entry page", same-day HEAD commit) adds:
- `internal/handlers/routes.go`: `v1.GET("/anizip-artwork/:id", h.HandleGetAnizipArtwork)`
- `internal/handlers/metadata.go`: `HandleGetAnizipArtwork` (+37 lines)
- `internal/api/anizip/anizip.go` + `anizip_helper.go`: server-side filecache (7d TTL) for ani.zip artwork URLs

This endpoint is **absent from `seanime-web/src/api/generated/endpoints.ts` and `types.ts` entirely** — not a tenji-lag issue, it was never run through codegen in web either. Confirmed by grep: web's own consumer (`seanime-web/src/app/(main)/_features/video-core/video-core-loading-screen.tsx:18-25`) hand-calls it:
```ts
export function useAnizipArtwork(mediaId: number | null | undefined) {
    return useServerQuery<AniZipArtwork>({
        endpoint: `/api/v1/anizip-artwork/${mediaId}`,
        method: "GET",
        queryKey: ["anizip-artwork", mediaId],
        enabled: !!mediaId,
        staleTime: Infinity,
    })
}
```
with a hand-written local type `export type AniZipArtwork = { fanart?: string, logo?: string, title?: string }` (line 15 of the same file) — bypasses `API_ENDPOINTS` registry and generated `types.ts` entirely, which is why codegen never picked it up. So this is a real, currently-shippable endpoint (`GET /api/v1/anizip-artwork/:id` → `{ fanart?, logo?, title? }`) with zero presence in tenji's API layer (checked: no `anizip-artwork` string anywhere in seanime-tenji/src). Tenji has no dedicated loading-screen/pre-playback artwork component analogous to `video-core-loading-screen.tsx` (searched `src` for loading-screen/backdrop/clearlogo-named files — only hits are unrelated hero-carousel components). This is a genuinely new, un-ported surface as of today's HEAD; report as missing (own item, not double-counted with whatever UX/loading-screen agent covers the visual behavior — this note is specifically about the fact that the underlying endpoint+type were hand-rolled and have zero tenji-side plumbing, so even a minimal port needs a new hook + type, nothing to "sync" from codegen).

### 6. Settings-redaction shape check (explicitly asked about in task brief)
Commit 7dec652f ("full-fork audit — security auth-gating... (3.9.5)") added `redactSettingsSecretsForNonAdmin` applied to `GET /settings`, `/status`, `/debrid/settings`. Checked whether this changed the **shape** of any generated type (it would show up in the types.ts diff already captured above) — it does not: settings-related structs (`Models_Settings`, debrid settings types, etc.) are unchanged in the diff. This is because redaction zeroes out secret field *values* server-side for non-admin callers at serialization time; it does not add/remove/rename fields. **No type drift, no tenji action needed for wire compatibility** — tenji already deserializes these structs correctly; a non-admin tenji user will simply start receiving blanked-out secret fields (correct/expected behavior matching web), nothing to fix.

## Summary table (raw)

| Cluster | Web has | Tenji has | Real gap? |
|---|---|---|---|
| DummyDebrid endpoints/types (2 endpoints, 3 types) | yes | no | No — test-only mock provider, not user-facing |
| `INTERNAL_FeatureFlags.dummyDebrid` | yes | no | No — internal flag paired with above |
| `DebridClient_PrewarmStatusItem` doc-comment reword | yes | stale comment (harmless, TS doesn't ship comments to runtime) | No — field shape identical, tenji already ported the behavior this describes |
| `MpvCore_ClientEventType` += `"startup-timing"` | yes | n/a | N/A — Electron/MpvCore-only perf event |
| `Nakama_WatchPartySessionMediaInfo.media?` | yes (unused by web frontend too) | no | No actionable gap — backend-only groundwork, tenji's room UI runs on a different (newer) nakama model anyway |
| `hooks_template.ts` / `queries_tmpl.ts` / `library_explorer.hooks.ts` diffs | — | — | No — scaffolding/CRLF noise only |
| `GET /api/v1/anizip-artwork/:id` (hand-authored, bypasses codegen) | yes, wired into loading screen (today's commit) | no | **Yes — genuinely missing**, own item |
| Settings-redaction (7dec652f) | shape unchanged | shape unchanged | No — pure server-side value redaction, no client change needed |

Net: after diffing 6 files totaling ~14.5k lines on each side, the *entire* mechanical drift between tenji's 3.9.1-alpha.3 generated snapshot and current 3.9.5 HEAD is: (1) an internal test-only debrid mock (not user-facing, correctly absent), and (2) one brand-new, non-codegen'd artwork-cache endpoint from today's HEAD commit that no client (including web's own generated layer) has formally synced yet. The generated API layer is otherwise in lockstep — no stale field names, no removed endpoints tenji still calls, no enum value tenji is missing that it actually needs.
