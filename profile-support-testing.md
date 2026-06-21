# Profile support — manual test checklist

Running checklist of everything to verify for the multi-user profile feature. Nothing
here has been tested yet (no runtime testing done during implementation). Work top to
bottom; **A → B** before the UI tests. Mark `[x]` as you go.

Generated admin password (the "random for now" one): `YsE4B21dlJ-bgNMDGlVz`

---

## A. Build & deploy (do first)

- [ ] **Local backend build**: `export GOROOT="$HOME/go-sdk/go" PATH="$GOROOT/bin:$PATH"; go build ./...` → no errors. *(passed during impl)*
- [ ] **arm64 deploy build** (per CLAUDE.md):
  ```
  cd seanime-web && npm run build && cd ..
  rm -rf web && mv seanime-web/out web
  export GOROOT="$HOME/go-sdk/go" PATH="$GOROOT/bin:$PATH"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o seanime-server-linux-arm64 .
  ```
- [ ] **Deploy** (scp + swap + `chcon -t bin_t` + restart — see CLAUDE.md runbook). `curl -s -o /dev/null -w "%{http_code}" https://seanime.clinshaiju.dev/` → `200`.
- [ ] **Migration safe**: server starts; existing settings/account/library intact; logs show `User`/`Session` tables auto-migrated, no errors.

## B. Admin bootstrap / cvslinc

Pick ONE:
- [ ] **Explicit**: deploy ran once with `./seanime --admin-username cvslinc --admin-password 'YsE4B21dlJ-bgNMDGlVz'` → log line `Admin credential set from flags`.
- [ ] **Auto-gen**: deploy ran with just `--admin-username cvslinc` (or no flags) → log line `Created initial admin user "cvslinc" with password: <X>` — copy `<X>`. (`journalctl -u seanime | grep -i admin`)
- [ ] Confirm exactly **one** admin exists, named `cvslinc`, and the existing AniList account is linked (its library/collection shows after login).

## C. Auth hardening — backend (curl, server password = `S`)

> `H='-H "X-Seanime-Token: <sha256(S)>"'` for the gate. Get a session: `POST /api/v1/user/login {username,password}` → `token`.

- [ ] **Login** `POST /api/v1/user/login` with cvslinc creds → `200 {token, user}`. Wrong creds → `401 invalid username or password`.
- [ ] **No session, config write blocked**: `PATCH /api/v1/settings` with only `X-Seanime-Token` (no Bearer) → **403** "admin privileges required".
- [ ] **With admin Bearer, config write allowed**: same PATCH + `Authorization: Bearer <token>` → succeeds.
- [ ] Repeat the 403/allow check for `PATCH /api/v1/debrid/settings`, `/torrentstream/settings`, `/mediastream/settings`, `POST /api/v1/start`.
- [ ] **Reads open**: `GET /api/v1/status`, `GET /api/v1/settings` work without a Bearer (only password).
- [ ] **Logout** `POST /api/v1/user/logout` (with Bearer) → token no longer resolves (subsequent admin write → 403).

## D. Auth hardening — local install (regression)

- [ ] On a **password-less** Seanime (desktop or `--disable-password`): everything works with NO login (operator implicitly admin). Settings still editable. No login screen appears.

## E. Frontend login flow (browser, on the VPS)

- [ ] Open the site → enter server password → **UserLoginScreen** ("Sign in") appears, NOT the app.
- [ ] Sign in as `cvslinc` → app loads; AniList collection/library present.
- [ ] **Refresh** the page → still signed in (session persisted in `localStorage["sea-session-token"]`).
- [ ] DevTools → Network: requests carry `Authorization: Bearer …`.
- [ ] Manually delete `sea-session-token` in localStorage + refresh → back to Sign in screen.

## F. Role gating + Users UI (browser, as admin)

- [ ] As cvslinc, **Settings → Users** tab visible; list shows `cvslinc` (ADMIN badge, no Delete button).
- [ ] **Create user**: username `testuser`, password (≥6 chars), role `User` → toast "User created"; appears in list with USER badge + Delete button.
- [ ] Duplicate username → error toast (no crash).
- [ ] **Delete** `testuser` → removed from list.

## G. Regular-user experience (browser)

- [ ] Create `testuser` again. Sign out of cvslinc *(no logout button yet — clear `sea-session-token` in localStorage + refresh)*, sign in as `testuser`.
- [ ] Status `userRole` = `user`. **Settings page shows "Server settings are managed by the administrator"** (no config forms).
- [ ] Browsing/playback still works for the regular user (shares the admin's AniList/library for now — see known gaps).
- [ ] Attempt a config write via API as testuser → 403.

---

## Known gaps — NOT bugs (don't report these as failures)

These are intentionally deferred to later phases:
- **Per-user settings are still global** — theme/UI/playback prefs are shared; a change by anyone affects everyone. (P6 + P2-backend)
- **AniList is shared** — all users currently see the admin's AniList account/collection. (P3)
- **No in-app user-logout button** — use localStorage clear or the AniList "Sign out". (added later, P3)
- **WebSocket events are broadcast** — scan/playback events not yet per-user. (P5)
- **Streaming is one global session** — two users streaming simultaneously will collide. (P4)
- **Secrets in status** — debrid API key etc. still returned to any authenticated client. (P2-backend)
- **No resource concurrency limits** — shared debrid/transcode can contend. (P8)

## Recovery (if locked out of admin)

Re-run the binary once with `--admin-username cvslinc --admin-password '<new>'` (create-or-update;
resets the first admin's username+password). Or edit the server password in config (the network gate).
