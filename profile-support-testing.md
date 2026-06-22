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

- [ ] **Wrong server password is rejected**: at the password screen, a wrong password shows "Incorrect password" and does NOT advance to the user-login screen. (Regression fix — was previously falling through.)
- [ ] Open the site → enter the correct server password → **UserLoginScreen** ("Sign in") appears, NOT the app.
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

## H. Per-user data — theme & playlists (P6, partial)

- [ ] As cvslinc, set a custom theme (colors/background). Create `testuser`, sign in as them → testuser sees the **default** theme, not cvslinc's. Change it → cvslinc's theme is unaffected when you switch back.
- [ ] Create a playlist as cvslinc; sign in as `testuser` → playlist list is **empty** (not cvslinc's). Create one as testuser; back as cvslinc → only cvslinc's shows.
- [ ] **Upgrade carryover**: after the first deploy, cvslinc still has the pre-existing theme (backfilled from the old single-tenant row).

## J. Settings role-gating (c9, tab-level)

- [ ] As **admin** (cvslinc): all settings tabs visible (incl. Users, Local Library, Torrent*, Debrid, Online Streaming, Nakama, Logs, Transcoding).
- [ ] As a **regular user** (bob): only App, User Interface, Video Playback, Desktop Media Player, External Player Link, Manga, Discord (+ Denshi on desktop) are visible. The admin tabs are **gone** (not just disabled).
- [ ] As bob, change a user-editable setting (e.g. an AniList view pref, a Discord toggle, or Manga default provider) and Save → "Settings saved", persists across refresh, and does NOT change what cvslinc sees.
- [ ] Known follow-ups (not bugs yet): the App and Manga tabs still show a few admin-only sub-options to users (section-level gating pending); "use server debrid" toggle, season-grouping-in-UI, and per-device video-playback are not done yet.

---

## I. WebSocket primitive (P5) — regression only

- [ ] App still connects to `/events` and real-time updates (scan progress, playback) work as before (the primitive must not regress WS).
- [ ] DevTools → Network → WS: the `/events` URL includes a `session=…` param when logged in. (No user-visible behavior change yet — events still broadcast.)

---

## J. Per-user AniList (P3 slice A) — NEW, verify this round

Multi-user AniList: each user now links their **own** AniList account and sees/edits
their **own** collection. Admin (cvslinc) is unchanged.

Best with **two AniList accounts** (or verify the isolation direction with one):
- [ ] As **cvslinc** (admin): link AniList → admin's collection/lists load as before. Library, Schedule, Discover all work.
- [ ] As **bob** (regular user), before linking: library/lists show empty/simulated (no crash); Discover/search still works.
- [ ] As **bob**, link a *different* AniList account → bob sees **bob's** collection, not cvslinc's. Viewer name/avatar (top-right) is bob's.
- [ ] Edit a list entry as bob (score/status/progress, or mark watched via online-stream progress) → updates **bob's** AniList list; cvslinc's is untouched.
- [ ] AniList **Stats** and **lists-page tag filters** show bob's data, not cvslinc's (cache-leak check).
- [ ] Switch back to cvslinc → still their own collection/stats. No cross-contamination either direction.
- [ ] Single-user sanity: admin only, no regular users → behaves exactly as before.

Known still-shared after this slice (expected, lands in slice B/C):
- Playback / resume positions / "currently watching" tracking still run through the
  shared (admin) modules — a regular user's *watch tracking* and *streaming* are not
  yet their own. Per-session playback + the streaming split are next.

---

## K. Per-session streaming + continuity (P3/P4) — DEPLOYED, verify this round

Backend is on sha `d0d1c8ed`. These all need **two users** (cvslinc admin + a regular
user, both with AniList linked) and, ideally, **two devices/windows at once**.

**Hardening regression (must still hold):**
- [ ] Browser: server password → user login → your data. Denshi (rebuilt build): server password prefilled → user login → your data. A client with NO user login sees empty.

**Per-user DEBRID simultaneous (the main fix):**
- [ ] cvslinc streams show A via Debrid; at the same time the regular user streams a **different** show B via Debrid. Both play independently.
- [ ] Each profile's "Currently watching" tag + progress popup shows **only its own** show — no bleed, including after a page reload on either side.
- [ ] Pressing "Currently watching" on one profile never jumps to the other's show.
- [ ] Stopping/cancelling one user's stream does not affect the other's.

**Per-user resume positions (continuity):**
- [ ] cvslinc watches show X to ~10 min; regular user watches the SAME show X to a different time. Each user's resume position is their own (reopening resumes at their own spot, not the other's).
- [ ] Toggling "watch continuity" off for one user doesn't change the other.

**Per-user built-in local-file playback (if you have a local library):**
- [ ] Two users play different local files simultaneously via the built-in player → independent, no bleed.

**Regression — solo / admin:**
- [ ] Your normal solo streaming (just cvslinc) works exactly as before (debrid, resume, progress→AniList).
- [ ] Local desktop Denshi pointed at the VPS still plays (events reach it).

**Expected NOT fixed yet (don't report as bugs):**
- **Torrent streaming** (non-debrid torrentstream) is still single/global — two users torrent-streaming at once will still bleed/collide. Debrid is the fixed path.
- Built-in **transcode** (mediastream) path still global.

---

## L. Anon browse-only + per-user debrid auto-select (DEPLOYED, sha 4facc61b)

**Anon may browse, not stream (security) — verified server-side, confirm in UI:**
- [ ] A client that knows the server password but is NOT logged in as a user (e.g. an old/non-login client, or before logging in): can browse Discover/library (empty), but attempting to start a debrid/torrent/built-in stream is rejected (403). Logged-in users stream normally. Local desktop (no server password) unaffected.

**Per-user debrid auto-select (custom vs server default):**
- [ ] As a regular user → Settings → Debrid → "Auto-select" card → toggle **"Use server default auto-select"** OFF → the profile editor button appears → open it → set resolution/language/codecs/ranking → save. (Save the toggle too.)
- [ ] That user's debrid auto-pick now follows THEIR profile; cvslinc's (admin) auto-pick still follows the server default. They don't affect each other.
- [ ] Toggle back ON → user falls back to the server default again.
- [ ] Admin's existing auto-select profile is unchanged (backfilled to admin on upgrade).

---

## M. Bug-fix round (from iOS/Denshi test feedback) — NOT yet deployed

Three issues were reported from testing (tenji anon + cvslinc/bob). Status of each:

**M1. Anon could still trigger torrent work (FIXED, server-verifiable).**
Root cause: only the *stream-start* handlers were guarded; the debrid/torrent *operation*
endpoints (file-previews → adds the torrent to debrid to read files, add-torrent, etc.)
were open, so an anon's "watch" attempt ran "selecting/adding torrent" before the final
start 403'd. Now `h.UserOnly` guards the debrid + torrentstream operation routes too.
- [ ] As anon (server password, no login): `POST /debrid/torrents/file-previews`,
  `/debrid/torrents`, `/torrentstream/torrent-file-previews` → **403**. (curl-checkable.)
- [ ] No "selecting/adding torrent" overlay appears for an anon at all — the attempt is
  rejected up front, not mid-flight.
- [ ] Logged-in user: all of the above still work.

**M2. cvslinc (admin) saw bob's "selecting/adding torrent" overlay (FIXED).**
Root cause: the debrid Repository is a singleton wired to the *admin-scoped* event
manager, so EVERY user's stream overlay/loader events were emitted to the admin. Now the
repo routes overlay events to the streaming user (`SessionEventsFunc` → `session.Events()`).
- [ ] bob streams via debrid → bob (not cvslinc) sees the "Selecting/Adding/Downloading"
  overlay. cvslinc sees nothing of bob's.
- [ ] cvslinc streams → cvslinc sees his own overlay. No cross-bleed either direction.

**M3. cvslinc saw NONE of his own currently-watching/progress; bob's progress kept
advancing after his player closed (NOT fixed — needs the two-client test loop).**
This is the playback-*tracking* plane (currently-watching badge + progress popup +
stop-on-close), which is still entangled with the global modules and depends on how each
client tags its `/events` WS connection (`?session=`). It couldn't be reproduced from
static analysis. Re-test AFTER M1/M2 are deployed:
- [ ] Does cvslinc see his own currently-watching/progress again once M2 is in? (M2 may
  or may not resolve it.)
- [ ] Does bob's progress stop when bob closes the player?
- If either still fails, it's the **torrentstream/tracking per-user pass** (the deferred,
  entangled work) — capture server logs during the repro so it can be fixed with evidence
  rather than blind.

---

## Known gaps — NOT bugs (don't report these as failures)

DONE since earlier rounds: Theme, playlists, AniList account/collection, per-user
settings overrides, anon-data hardening (default), per-user **debrid** streaming +
serve routing, per-user **continuity** (resume positions). In-app user-logout exists
(sidebar). Debrid API key redacted from non-admins. WS events for the per-session
modules are now user-scoped.

Still deferred (NOT bugs):
- **Torrent streaming (torrentstream) is still global** — two users torrent-streaming
  simultaneously will bleed/collide. Needs the same per-session pass as debrid PLUS
  re-architecting its singleton playback-tracking loops (the entangled part). (next)
- **Built-in transcode (mediastream) path still global** — lower priority; debrid
  direct-play doesn't use it.
- **Auto-select profiles still shared** — and there's local uncommitted WIP in
  `internal/torrents/autoselect/`; left untouched.
- **No resource concurrency limits** — now that debrid can run N per-user streams, the
  shared debrid HTTP budget / transcoder could contend under load. Add semaphores when
  it matters (deadlock-sensitive; wants a tested session). (P8)
- **Watch-party rooms** not started (P7).
- **Per-session modules accumulate per user** (no eviction) — fine for a small group.
- **Per-session module settings are snapshot at build** — if a user changes
  watch-continuity / autoplay after their session is built, it updates on next session
  rebuild (re-login), not live. Minor.

## Recovery (if locked out of admin)

Re-run the binary once with `--admin-username cvslinc --admin-password '<new>'` (create-or-update;
resets the first admin's username+password). Or edit the server password in config (the network gate).
