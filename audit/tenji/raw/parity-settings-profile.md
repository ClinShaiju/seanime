# Parity audit — Settings / Account / Profiles (Tenji vs seanime-web/Denshi)

Scope: seanime-web settings pages (playback, debrid per-user, security/password, UI prefs),
profile/multi-user UI, the 3.9.5 audit commit 7dec652f client-side changes, vs tenji's
`features/profile` + `user-auth` screens. Auto-select editor and auto-downloader manager are
explicitly out of scope (known-deferred, confirmed still absent, not reported as new).

## Baseline re-verified (already ported per prior batch, spot-checked only)
- Per-user password change (`account.tsx` `useUserChangePassword`) — matches web change-password flow.
- Per-user debrid override (`account.tsx` debrid form) — field-for-field match with
  `seanime-web/src/app/(main)/settings/_containers/user-debrid-settings.tsx` (useServerDebrid,
  provider select, apiKey blank-keeps-existing, useServerAutoSelect). Auto-select profile editor
  correctly NOT rendered when useServerAutoSelect=false, matching web's own deferral pattern —
  confirmed still deferred-known, not a new gap.
- Per-user login/logout (`user-login-screen.tsx`, `useUserLogin`/`useUserLogout` in
  `user-auth.hooks.ts`) — distinct from AniList auth, mirrors web's UserLoginScreen used on
  networked servers with anon-data hardening.
- External player link/picker (`ExternalPlayerPickerSheet` in profile index.tsx "Player" section)
  covers the applicable portion of web's playback-settings.tsx "External Player" radio option.

## 7dec652f settings.go/status.go changes
Purely server-side (`redactSettingsSecretsForNonAdmin` blanking torrent/VLC/nakama passwords for
non-admins in HandleGetSettings + NewStatus). No client porting needed — both web and tenji just
consume whatever the server returns; nothing to build client-side. Confirmed via diff read.

## Investigated and confirmed NOT gaps
- "Local Account" web tab (`local-settings.tsx`, autoSyncToLocalAccount + Upload-to-AniList
  button) — its TabsTrigger is commented out in `page.tsx` (dead/unreachable nav item in web
  itself), so not a real tenji parity gap.
- Electron/Denshi-only playback engine settings (VideoCore vs MpvCore radio, mpv logging switch,
  custom mpv.conf textarea in `playback-settings.tsx`) — n/a, tenji always uses its own native
  expo-mpv-player, no engine choice exists to expose.
- `ui-settings.tsx` (1244 lines) theming: custom CSS, banner/background images+opacity+position,
  sidebar/navbar transparency/hover-expand, color theme bank, nav preloading mode — all DOM/desktop-
  web-specific, n/a for a native app with its own fixed design language.
- `ui-settings.tsx` sort-order defaults (`continueWatchingDefaultSorting`,
  `animeLibraryCollectionDefaultSorting`, `mangaLibraryCollectionDefaultSorting`) and count-display
  toggles (`showAnimeUnwatchedCount`, `showMangaUnreadCount`, `hideEpisodeCardDescription`, etc.) —
  grepped tenji: these ARE read and respected (`src/lib/theme-settings.ts`,
  `media-entry-card.tsx`, `use-anime-library-collection.ts`, `use-manga-library-collection.ts`)
  as server-pushed theme settings, just not locally editable (no tenji settings screen for them).
  Consistent with "theming is edited via web/admin, tenji is a fixed-design native consumer" —
  not treated as a gap; editing UI for these is intentionally n/a on this client the same way the
  rest of ui-settings.tsx is.

## Confirmed gaps

### 1. Admin user management (add/list/delete server users) — MISSING
- webRef: `seanime-web/src/app/(main)/settings/_containers/users-settings.tsx` — full CRUD:
  `useListUsers`, `useRegisterUser` (username/password min 6/role), delete via
  `DELETE /api/v1/user/{id}`, admin-only tab gated in `page.tsx`.
- tenjiRef (absence): grepped `useListUsers|useRegisterUser|user/list|user/register|RegisterUser|
  ListUsers` across tenji `app`/`src` — only hits are in **generated** stub files
  (`src/api/generated/endpoint.types.ts:2321`, `endpoints.ts:2487,2492` — API surface exists,
  unconsumed). Also grepped `isAdmin|userRole` usage across tenji: only 3 files
  (`app/(app)/(tabs)/(profile)/index.tsx`, `src/api/components/server-data-wrapper.tsx`,
  generated types) — index.tsx uses userRole only to gate the "Account"/"Sign out" section, no
  admin-only branch anywhere for user management.
- impact: An admin using Tenji as their only/primary client on a multi-user server (plausible per
  the profile-multiuser-support design — one admin, several users) has no way to add, list, or
  remove server user accounts from the phone; must fall back to web.
- applicability: legitimate — multi-user profile support is a real, shipped server feature, and a
  phone-only admin is a realistic scenario.
- effort: M — one list screen (reuse ProfileMenuSection/ProfileMenuItem patterns) + an add-user
  form (username/password/role, mirrors account.tsx's form patterns) + delete confirm (Alert,
  same pattern as handleSignOut). No new primitives needed.

### 2. AniList connect/disconnect (per-profile "Integrations") — MISSING
- webRef: `seanime-web/src/app/(main)/settings/_containers/integrations-settings.tsx` — per-user
  tab (visible to admin AND regular users) showing "Connect with AniList" (opens
  `isLoginModalOpenAtom` login modal) or, if connected, the linked username + "Disconnect" button
  (`useLogout` → `/api/v1/auth/logout`, confirm dialog "unlinked from this profile, reconnect
  anytime").
- tenjiRef (absence): `useLogin`/`useLogout` are fully implemented in
  `src/api/hooks/auth.hooks.ts` (invalidate library/collection queries, call setServerStatus) but
  grepping `useLogout\b`/`useLogin\b` across all of `app`+`src` (excluding the hook-definition
  files) turns up **zero real call sites** — only an unused template stub
  (`src/api/generated/hooks_template.ts:334`). Also zero hits for
  `anilist.co/api/v2/oauth|auth/callback|LoginModal|isLoginModalOpenAtom`. Confirmed via
  `app/(out)/set-server-url.tsx` (the only onboarding/server-connect screen, full file read) that
  onboarding is server-URL + optional server-password only — no AniList OAuth/token step anywhere
  in tenji, at onboarding or afterward. `account.tsx` (the per-user profile-management screen) has
  password + debrid forms only, no AniList connect/disconnect section.
- impact: A profile whose AniList account isn't yet linked (fresh non-admin user created via
  server-side registration, or a profile that was disconnected/token-expired) has literally no way
  to link/relink AniList from within Tenji — it would need the web UI. Since AniList linkage
  drives list sync, this affects real per-user functionality on multi-user servers, not just a
  cosmetic settings gap.
- applicability: legitimate for the multi-user profile scenario; for a single-admin
  already-linked-via-web-once server this is lower-frequency (mainly token-expiry re-auth).
- effort: M — needs whatever the "login modal" token-paste/OAuth flow actually is on the server
  side wired into a native screen (not deeply inspected end-to-end here, this was scoped as a
  presence/absence check); could be L if the underlying auth flow requires an embedded webview
  OAuth redirect rather than a simple token paste.

## Not investigated / left for other lanes
- Admin-only server-machine settings tabs (torrent-client, mediastream, library scan config,
  onlinestream, nakama, logs) gated in web's `page.tsx` — these look like server-admin config
  more naturally owned by a different audit lane (torrent/debrid or server-admin), not
  "account/profile" in the strict sense; not reported here to avoid duplicate findings.
- `mediaplayer-settings.tsx` external-player-link custom URI-scheme configuration — only inferred
  parity via ExternalPlayerPickerSheet, not diffed field-by-field.
