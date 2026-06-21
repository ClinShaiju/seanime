# Per-user settings split — spec (from testing feedback, 2026-06-21)

The granular version of "role-gated settings" (was P2-backend, deferred). Locked
settings are **fully hidden** from non-admins (not shown disabled). This is the
contract for which settings each role controls.

## Per-tab disposition

| Tab / section | Regular user | Notes |
|---|---|---|
| **App** | editable EXCEPT: episode/default playback source, extension secure mode, and the app/update "complete app" section | most of App is user-facing |
| **User Interface** | **editable** (full) | |
| **Season grouping** | **editable** — MOVE it out of Local Anime Library INTO User Interface | currently lives in Local Anime Library |
| **Local Anime Library** | locked (after season grouping moved out) | server/library paths/scanner |
| **Video Playback** | **per-device/client** — store client-side, not in server Settings | not a server value at all |
| **Desktop Media Player** | **editable** | client-side anyway |
| **External Player Link** | **editable** | client-side |
| **Transcoding / Direct Play** (mediastream) | locked | admin/server |
| **Torrent Provider** | locked | |
| **Torrent Client** | locked | |
| **Torrent Streaming** | locked | |
| **Debrid Service** | locked EXCEPT new **"Use server debrid"** toggle (default ON) | if OFF, user supplies own provider+key; everything else in debrid stays locked |
| **Online Streaming** | locked | |
| **Manga** | **editable** EXCEPT local provider (locked) | |
| **Nakama** | locked | |
| **Discord** | **editable** | client-side RP |
| **Denshi** | **editable** | client app |
| **Logs & Cache** | **removed / not visible** | hide the tab entirely for users |

## New mechanisms this requires (beyond a plain field split)

1. **Per-device Video Playback settings** — move these out of the server `Settings`
   blob to client-side (localStorage) per device. Server stops owning them.
2. **"Use server debrid" (per-user, default ON)** — when OFF, the user provides their
   own debrid provider + API key, and *their* streams resolve through *their* debrid
   instead of the shared admin one.
   - ARCHITECTURAL NOTE: this means a **per-user debrid client** for opted-out users,
     which departs from the pure "shared content plane" model and couples to the
     per-user streaming work (P4). Needs design: store per-user debrid creds
     (UserSettings), and have the debrid stream path pick the user's client when set.

## Implementation outline

- **Backend:** new `UserSettings` table (FK user_id) holding the user-editable fields
  pulled out of `models.Settings` (App-subset, Anilist view prefs, Discord, Notifications,
  Manga-provider, season grouping flags, default playback source? no—that's locked,
  per-user debrid override). Keep `Settings` as the admin/server row. Per-user GET/PATCH
  endpoints scoped by `dataUserID`; admin endpoints stay `AdminOnly`. Migrate/seed.
- **Status:** return the merged effective settings for the acting user (server fields +
  their user overrides), and omit/redact locked fields for non-admins (also fixes the
  "secrets in status" gap — don't send debrid key etc. to non-admins).
- **Frontend:** settings page renders per-tab by role; locked tabs/sections hidden.
  Move season-grouping control into the UI tab. Video Playback reads/writes localStorage.
- **Client-side (Video Playback / external player / desktop player / Denshi):** these
  are per-device; the server stores nothing (or stores opaquely). Overlaps P9.

## Open decisions
- Per-user debrid: confirm we want per-user debrid clients (yes per user request) —
  schedule with P4 (per-user streaming) since that's where the stream path becomes per-user.
- "complete app section" in the App tab — confirm exactly which controls stay admin.
