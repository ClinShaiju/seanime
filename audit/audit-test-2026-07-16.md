# Audit fix — manual test guide (2026-07-16)

Companion to [`all-features-2026-07-16.md`](./all-features-2026-07-16.md). That doc says *what was wrong*;
this one says *how to prove the fixes work* before you ship them.

**State of the tree:** 39 files changed, uncommitted, **not deployed**. Backup of the full working diff:
`<scratchpad>/audit-fixes-backup.patch`.

---

## 0. Read this first — what is and isn't already verified

Be selective about your time. This is the honest split.

### Already verified — do NOT re-test by hand

| | Evidence |
|---|---|
| Compiles + typechecks | `scripts/check.sh` → `GO BUILD OK` / `WEB TYPECHECK OK` |
| Static analysis | `go vet ./internal/...` **completely silent** (8 diagnostics at session start → 0) |
| nakama locks (NAK-3/NAK-5) | `ok seanime/internal/nakama` |
| websocket (EVT-1/EVT-2) | `ok seanime/internal/events`, incl. a **`-race`** test + 2 new regression tests |
| session leaks (MC-2/MC-3/DB-1) | `ok` for mediacore/videocore/core/shared_platform, plus goroutine-leak tests that were **falsified** (reverting the ticker fix made them correctly report `LEAK: startQueuedUpdateSync still running after Close()`) |
| filecache (DB-4) | `ok seanime/internal/util/filecache` |
| goja `Close()` drain (PLG-4) | differential test: original panicked `send on closed channel`; fixed version settled the promise |

### Also verified — DYNAMICALLY, against real booted servers (this list grew a lot; read it before testing)

A later dynamic pass booted real servers on isolated datadirs/ports and drove them. **Each probe below was
proven to actually FAIL on the old code** — a test that cannot fail proves nothing.

| | Evidence |
|---|---|
| **AUTH-1..4 + AUTH-5** | Full 14-cell matrix (anon/user/admin) observed live, token correctness proven first (401/401/200) so it isn't a vacuous all-403 pass; all **24** `UserOnly`/`guardStreamingUser` sites swept; hostile extension id (`../../evil`) rejected with **no file written**; **password-less desktop 14/14 unaffected** |
| **SYNC-1, SYNC-2, TS-1, PLAYLIST-1, NAK-5, DB-3** | `-race` **CLEAN + FALSIFIED** — each fix reverted → detector fires → restored md5-identical. SYNC-1's falsification reproduced the exact `handlers/local.go:160` HTTP-handler crash |
| **R1** | Same probe **HANGS 8s** on pre-fix code, PASSES on fixed; plus 60 reloads / 30 HTTP `fetch()` calls, all <200ms |
| **PLG-4** | fetch pump **flat 37→36** over 60 reloads, with a natural control in the same profile (chromedp grew 37→427) |
| **EVT-1, EVT-2** | `-race` fired on the pre-fix pattern; old code proven to evict the **live** tab; 12 same-id conns / 8 closes → zero ghosts; sidecar survives churn |
| **DB-1** | ticker returns to baseline across 8 sessions + 32 evictions |
| **Manga trio** | fd **+0** over 50×150 pages (control: +1500 exactly linear); cap peak **5** vs **60**; zero data races |

### STILL NOT verified — this guide is the only thing standing between these and prod

| Gap | Why |
|---|---|
| **MC-2, MC-3** | **`NOT_RUN`.** These build **lazily, per session, only on real playback** — 8 sessions + 32 evictions never instantiated a per-user coordinator/videocore. Cannot be driven headlessly. Rest on code review + falsified unit leak tests. **P0 manual.** |
| **DBG-1** | Needs real debrid provider credentials + Denshi. A false positive here is a hard playback failure with no cold-resolve fallback. **P0 manual.** |
| **TS-1 / PLAYLIST-1 end-to-end** | Race-falsified at unit level, but TS-1's falsification used **transcribed stand-ins** on both sides (driving production needs a live torrent client). Real torrent streaming + playlist next/stop is unproven. |
| **MERGE-1, all UI gating** | Needs a Denshi rebuild / a browser with two real accounts. |
| **VET-1 (ScanLogger)** | `scanner` suite still fixture-blocked (`missing AniList fixture`). |

**So: `/vloop`'s static half passed for every fix, and its dynamic half now passes for most.** What remains is
genuinely un-simulatable here: real playback, real debrid, real torrents, and a real Denshi build.

---

## 1. What each test actually requires

Don't start until you know which of these you need — several fixes are **not testable** in a browser.

- **Server redeploy** (`scripts/deploy-server.sh`) — reaches prod for any Go-side fix.
- **Denshi rebuild** (`scripts/build-denshi-local.sh --installer`) — **required for any WEB change to reach
  Denshi.** Denshi bundles its own UI; a server redeploy never updates it. Playback/player changes
  (MpvCore, MERGE-1, debrid playback) are **Denshi-only** — the browser has no MpvCore.
- **Two distinct accounts** — one admin, one `role="user"`. The auth-gate tests are meaningless without this,
  and a local password-less install makes *everyone* admin (`IdentityMiddleware` injects the admin;
  `useIsAdmin()` returns true), so those tests must run against the networked/password setup.
- **Two distinct accounts for nakama** — a known trap: testing a watch room with the *same* account on both
  ends silently does nothing (one participant slot, no relay targets).
- **A debrid provider** — and note TorBox vs RealDebrid/Premiumize behave *differently* for DBG-1 by design.

### Two failure modes are silent — know them before you start

1. **A server-process death.** SYNC-1 and NAK-5 fixed fatal Go runtime throws (`concurrent map read and map
   write` / `concurrent map iteration and map write`). These are **not** recoverable panics — they kill the
   whole process. If a fix were wrong, the symptom is the server vanishing and systemd restarting
   `seanime.service`, not an error toast.
2. **A hang, not an error.** An R1-style failure leaves a promise unsettled — search spins forever with no
   error logged. "No error in the log" does not mean "working".

---

## 2. Test cases

<!-- generated per-area cases appended below -->
_30 cases, ordered by priority. **P0 = do not deploy without these.**_

| # | case | covers | priority | only manual verification? |
|---|------|--------|----------|---------------------------|
| T-AUTH-1 | Non-admin is blocked from server-wide torrent-client actions (add / pause-all /  | AUTH-1, AUTH-2 | P0-blocker | **YES** |
| T-AUTH-2 | Admin still has full torrent-client control (regression check) | AUTH-1, AUTH-2 | P0-blocker | no |
| T-AUTH-3 | Non-admin cannot mutate Auto Downloader (rules/profiles/run/item-delete) — sideb | AUTH-3 | P0-blocker | **YES** |
| T-AUTH-4 | Admin still has full Auto Downloader control (regression check) | AUTH-3 | P0-blocker | no |
| T-AUTH-6 | Local password-less install is unaffected — single operator remains admin everyw | AUTH-1, AUTH-2, AUTH-3, AUTH-4 | P0-blocker | no |
| T-EXT-R1-BULK-RELOAD-FETCH | Bulk 'reload external extensions' must not hang provider fetch() (THE regression | R1 | P0-blocker | **YES** |
| T-EXT-R1-REPEAT | Bulk reload survives repeated invocations (not just the first one) | R1 | P0-blocker | **YES** |
| T-DBG-1-REGRESSION | Continue-Watching / Next-Episode preloaded playback still plays normally (regres | DBG-1 | P0-blocker | **YES** |
| T-PLAYLIST-1 | Playlist end-to-end lifecycle: multi-episode, next, STOP mid-episode, failed epi | PLAYLIST-1 | P0-blocker | **YES** |
| T-PLAYLIST-2 | Rapid playlist restart does not kill the new session (sessionGen regression chec | PLAYLIST-1 | P0-blocker | **YES** |
| T-MC-SESSION-EVICT | AniList disconnect/reconnect (session eviction) does not kill live playback sign | MC-2, MC-3, DB-1 | P0-blocker | **YES** |
| T-NAK-1 | Watch-party host/peer session churn must not crash the server (NAK-5 map-marshal | NAK-5, NAK-3 | P0-blocker | no |
| T-SYNC-1 | Polling the local-sync queue endpoint during an active sync must not crash the s | SYNC-1, SYNC-2 | P0-blocker | **YES** |
| T-AUTH-5 | Extension user-config: non-admin has no visible entry point and is 403'd server- | AUTH-4 | P1-important | **YES** |
| T-EXT-PLG4-GOROUTINE-LEAK | Plugin unload actually reclaims its fetch pump goroutines (the original PLG-4 le | PLG-4 | P1-important | **YES** |
| T-EXT-PLG4-INFLIGHT-SETTLES | An in-flight fetch() at unload time still resolves/rejects instead of leaving th | PLG-4 | P1-important | no |
| T-DBG-1-PACK | Multi-file pack/batch torrent: two preloaded episodes from the SAME torrentItemI | DBG-1 | P1-important | **YES** |
| T-DBG-1-NONTORBOX | Non-TorBox provider (RealDebrid/Premiumize) preloaded playback is completely una | DBG-1 | P1-important | **YES** |
| T-TS-1 | Torrentstream start/stop/restart cycling and shutdown-mid-stream stability | TS-1 | P1-important | **YES** |
| T-VC-REGRESSION | Live (non-evicted) browser playback: subtitle upload + InSight characters still  | MC-3 | P1-important | **YES** |
| T-EVT-1 | Closing one browser tab must not silently kill another live tab's websocket even | EVT-2, EVT-1 | P1-important | no |
| T-MANGA-1 | Long remote-provider chapter in Double Page mode: correct pairing + no unbounded | MEDI-NAK-2 (5-slot semaphore on getPageDimensions), MEDI-NAK-3 (race-free per-goroutine error via local ferr/b instead of the shared named return err) | P1-important | **YES** |
| T-MANGA-2 | Local manga provider: .cbz/.zip AND plain-directory chapters load correctly acro | MEDI-NAK-4 (page.Close() added in both branches of local.go FindChapterPages) | P1-important | **YES** |
| T-MERGE-1 | Denshi: seek-bar OP/ED highlight now matches what auto-skip actually skips | MERG-MERGE-1 (mpv-core-time-range.tsx skipChapters memo now passes {guardIntro:false, heuristics:true, duration}) | P1-important | **YES** |
| T-EXT-PLG4-PANIC-ISOLATION | One panicking fetch response no longer kills the pump for every later fetch on t | PLG-4 | P2-nice | **YES** |
| T-DBG-1-RESTART | Playback after a server restart (cold in-memory fileIdCache) still plays via the | DBG-1 | P2-nice | **YES** |
| T-DBG-1-COLD-CONTROL | Fresh cold-resolve play (never preloaded) as a control/baseline — isolates wheth | DBG-1 | P2-nice | **YES** |
| T-SCAN-1 | Library scan still logs cleanly after ScanLogger.mu removal (VET-1 regression ch | VET-1 | P2-nice | **YES** |
| T-DB-3 | Login/logout/account-switch still work without hanging after accountCache gets a | DATA-DB-3 (accountCacheMu RWMutex guarding the package-level accountCache; lock NOT held across GetAdminUser/GetAccountByID/gormdb calls) | P2-nice | **YES** |
| T-DB-4 | Mediastream/transcode still functions after filecache TrimMediastreamVideoFiles  | DATA-DB-4 (c.stores wipe moved inside the `if len(files) > 10` block; `return err` -> `return nil`) | P2-nice | **YES** |


### T-AUTH-1 — Non-admin is blocked from server-wide torrent-client actions (add / pause-all / resume-all / speed limits / change save path) but keeps per-torrent control

**Covers:** AUTH-1, AUTH-2  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Networked server with a password set (deploy-server.sh to the Pi, or run seanime locally with Server.Password configured — password-less local installs make everyone admin and cannot exercise this). Browser UI is enough — no Denshi rebuild needed (torrent-client/page.tsx is a web page Denshi does not bundle its own copy of... actually it does bundle web UI, so also verify in Denshi if that surface matters there). Two accounts: the existing admin, plus a fresh role=user account created via Settings > Users (users-settings.tsx 'Add a user' form, Role=User).

**Preconditions:** At least one active torrent in the built-in torrent client (seanime torrent-client) so per-torrent buttons are visible; a torrent provider configured (TORRENT_PROVIDER != NONE) so the sidebar shows the Auto Downloader entry is irrelevant here but torrent client works.

**Steps:**

1. Log in as the non-admin user (username/password created in Settings > Users). 2. Navigate to /torrent-client. Confirm the page renders (title 'Torrent client', torrent list). 3. In the header, confirm 'Add torrent', 'Pause all', 'Resume all' buttons are ABSENT (torrent-client/page.tsx wraps them in `{isAdmin && ...}`). 4. On any listed torrent's action row, confirm 'Change save path' icon button and the speed-limits Popover ('Apply limits') are ABSENT; confirm Force start, Reannounce, and per-torrent Pause/Resume icon buttons (BiPlay/BiPause) ARE present and clickable. 5. Right-click a torrent to open the context menu; confirm 'Change save path' menu item is ABSENT but 'Rename' and 'Remove' remain. 6. Click Pause then Resume on a specific torrent and confirm it actually toggles (proves per-torrent action still reaches HandleTorrentClientAction unblocked). 7. Force the gated path directly: with the non-admin's bearer session, POST /api/v1/torrent-client/action with body {"action":"pause-all"} (or resume-all/set-limits/move-storage/add-magnet). Expect HTTP 403.

**PASS looks like:** Non-admin sees the torrent list and can pause/resume/remove individual torrents, but the 5 server-wide controls (Add torrent, Pause all, Resume all, Change save path, Apply limits) are not rendered anywhere (list header, per-row actions, context menu). A direct POST for any of pause-all/resume-all/set-limits/move-storage/add-magnet returns 403 with body {"error":"admin privileges required"} (errAdminRequired via h.RequireAdmin inside HandleTorrentClientAction, torrent_client.go).

**FAIL looks like:** Either (a) any of the 5 admin-only buttons/menu items is still visible/clickable for the non-admin account (isAdmin wrap missing/reverted in torrent-client/page.tsx), or (b) the direct POST for pause-all/resume-all/set-limits/move-storage/add-magnet returns 200 instead of 403 (the `switch b.Action { case "pause-all", "resume-all", "set-limits", "move-storage", "add-magnet": RequireAdmin }` block was removed from HandleTorrentClientAction, torrent_client.go), or (c) per-torrent pause/resume silently fails or 403s for the non-admin (UserOnly middleware over-tightened to AdminOnly on the /torrent-client/action route in routes.go, which would also break password-less clients — see AUTH-2 regression risk note).

**If it fails, look here first:** internal/handlers/torrent_client.go HandleTorrentClientAction — check the `switch b.Action` RequireAdmin block right after the b.Action=="" validation; internal/handlers/routes.go line ~319 (`v1.POST("/torrent-client/action", h.HandleTorrentClientAction, h.UserOnly)`) — must be UserOnly not AdminOnly at the route; seanime-web/src/app/(main)/torrent-client/page.tsx `Dashboard()` — check `isAdmin` wraps around the header buttons and the Change-save-path Tooltip/Popover/ContextMenuItem.


### T-AUTH-2 — Admin still has full torrent-client control (regression check)

**Covers:** AUTH-1, AUTH-2  |  **Priority:** P0-blocker  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** Same networked/password server as T-AUTH-1, logged in as the admin account. Browser UI only.

**Preconditions:** At least one active or paused torrent present.

**Steps:**

1. Log in as admin, go to /torrent-client. 2. Confirm 'Add torrent', 'Pause all', 'Resume all' are visible in the header and functional (add a magnet, pause all, resume all). 3. On a torrent row, confirm 'Change save path' icon and the speed-limits popover are visible; open the popover, set a download/upload limit, click 'Apply limits' and confirm it succeeds (toast/no error). 4. Right-click a torrent, confirm 'Change save path' context-menu item is present and works. 5. Add a magnet with an absolute destination path via 'Add torrent' and confirm it downloads (server-side add-magnet path validation — b.Dir required, must be absolute — should not reject a normal library path).

**PASS looks like:** Every torrent-client control that worked pre-fix still works for the admin: add torrent, pause-all, resume-all, per-torrent change-save-path, and speed limits all succeed with no 403.

**FAIL looks like:** Any of these returns 403 for the admin account (e.g. toast 'admin privileges required' or console 403 on /api/v1/torrent-client/action), or the buttons are hidden even for the admin — indicates `useIsAdmin()` is returning false for the admin session (serverStatus.userRole not 'admin', or serverHasPassword miscomputed) or `h.IsAdmin(c)`/`h.RequireAdmin` resolving the wrong role server-side.

**If it fails, look here first:** seanime-web/src/app/(main)/_hooks/use-server-status.ts useIsAdmin() — check serverStatus.userRole is actually 'admin' for this session (Network tab on GET /api/v1/status); internal/handlers/identity.go CurrentUserRole/IsAdmin — check the session's role resolution.


### T-AUTH-3 — Non-admin cannot mutate Auto Downloader (rules/profiles/run/item-delete) — sidebar entry and page both gated, reads still work

**Covers:** AUTH-3  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Networked server with password set (Pi or local-with-password). Two accounts as in T-AUTH-1. Browser UI (no Denshi rebuild needed to validate the concept, but if validating Denshi specifically it needs build-denshi-local.sh since Denshi bundles its own web UI).

**Preconditions:** Server has a torrent provider configured and libraryPath set (so the Auto Downloader nav entry would show at all for an admin) and at least one existing auto-downloader rule created by the admin beforehand.

**Steps:**

1. Log in as admin, create at least one auto-downloader rule for any anime (so there's something for the non-admin to read). 2. Log out, log in as the non-admin user. 3. Check the left sidebar: 'Auto Downloader' entry must be ABSENT (main-sidebar.tsx now requires `isAdmin && ...` before adding the nav item). 4. Navigate directly to /auto-downloader by URL. Confirm the page renders a 'Admin only' LuffyError ('Only the server admin can manage the Auto Downloader.') instead of the AutoDownloaderPage content. 5. Open any anime's detail page, find the auto-downloader rule button/panel (anime-auto-downloader-button.tsx) — confirm the '+' create-rule button/modal trigger is ABSENT, but the existing rule (if one applies to that anime) is still LISTED read-only: the AutoDownloaderRuleItem row shows but is not clickable (no cursor-pointer, no chevron icon) — clicking it must not open the edit modal. 6. Directly call the mutation endpoints with the non-admin's session: POST /api/v1/auto-downloader/rule, PATCH /api/v1/auto-downloader/rule, DELETE /api/v1/auto-downloader/rule/:id, POST /api/v1/auto-downloader/run, DELETE /api/v1/auto-downloader/item. Expect 403 on all. 7. Confirm GET /api/v1/auto-downloader/rules and GET /api/v1/auto-downloader/items still return 200 for the non-admin (reads stay open).

**PASS looks like:** Non-admin sees no Auto Downloader nav entry, gets an 'Admin only' message on direct navigation to /auto-downloader, sees existing rules read-only (no chevron, not clickable) on anime pages, and every mutation endpoint 403s while GET rules/items still succeed.

**FAIL looks like:** Any mutation endpoint (create/update/delete rule or profile, run, run/simulation, delete item) returns 200/201 for the non-admin instead of 403 with 'admin privileges required' — meaning `h.AdminOnly` is missing from that route in routes.go; or the sidebar/page/rule-item still expose clickable create/edit controls to the non-admin (isAdmin checks stripped from main-sidebar.tsx, auto-downloader/page.tsx, anime-auto-downloader-button.tsx, or autodownloader-rule-item.tsx); or GET rules/items now also 403 for the non-admin (accidentally used AdminOnly instead of leaving GET ungated — this is the documented AUTH-3 regression risk of over-gating with UserOnly/AdminOnly on reads).

**If it fails, look here first:** internal/handlers/routes.go ~lines 184-200 — verify every mutation route (`/auto-downloader/rule` POST/PATCH, `/rule/:id` DELETE, `/profile` POST/PATCH, `/profile/:id` DELETE, `/run`, `/run/simulation`, `/item` DELETE) carries `h.AdminOnly` and every GET route does not; seanime-web/src/app/(main)/_features/navigation/main-sidebar.tsx `isAdmin &&` around the Auto Downloader nav item; seanime-web/src/app/(main)/auto-downloader/page.tsx top-level `isAdmin ? <AutoDownloaderPage/> : <LuffyError/>`; autodownloader-rule-item.tsx `isAdmin` gating the chevron/onClick.


### T-AUTH-4 — Admin still has full Auto Downloader control (regression check)

**Covers:** AUTH-3  |  **Priority:** P0-blocker  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** Same server as T-AUTH-3, logged in as admin.

**Preconditions:** Torrent provider + libraryPath configured so the nav entry is eligible to appear at all.

**Steps:**

1. Log in as admin. Confirm 'Auto Downloader' appears in the sidebar and /auto-downloader loads the full AutoDownloaderPage (not the 'Admin only' error). 2. Create a new rule, edit it, and delete it — all should succeed. 3. On an anime's detail page, click the '+' to create a rule for that anime, then open an existing rule row (chevron + click should open the edit modal) and delete it. 4. Click 'Run' (HandleRunAutoDownloader) if exposed in the UI, or POST /api/v1/auto-downloader/run directly as admin — expect 200.

**PASS looks like:** All auto-downloader CRUD and run operations succeed for the admin exactly as before the fix; nav entry and page render normally.

**FAIL looks like:** Admin sees the 'Admin only' LuffyError on /auto-downloader, or any create/update/delete/run call 403s for the admin session — indicates useIsAdmin()/IsAdmin(c) role resolution is broken for admin, not just non-admin.

**If it fails, look here first:** Same files as T-AUTH-3's if_it_fails; specifically check the admin session's serverStatus.userRole via GET /api/v1/status and h.IsAdmin(c)/CurrentUserRole server-side.


### T-AUTH-6 — Local password-less install is unaffected — single operator remains admin everywhere

**Covers:** AUTH-1, AUTH-2, AUTH-3, AUTH-4  |  **Priority:** P0-blocker  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** A local build/run of seanime with NO server password configured (fresh local install or an existing local install with Server.Password empty) — the default single-user desktop use case, including Denshi run via build-denshi-local.sh unpacked exe.

**Preconditions:** None beyond a password-less server (IdentityMiddleware injects the admin identity for every request per identity.go 'Networked server, no session -> no identity' vs local fallback).

**Steps:**

1. Open the app (browser at localhost, or Denshi unpacked) with no server password set. 2. Confirm the sidebar shows 'Auto Downloader'. 3. Confirm /auto-downloader loads the full page, and rule create/edit/delete all work. 4. Confirm /torrent-client shows Add torrent/Pause all/Resume all/Change save path/speed limits, and they work. 5. Confirm any extension's Preferences gear is visible and editable.

**PASS looks like:** Every control behaves exactly as before this fix — nothing is hidden, no 403s — because useIsAdmin() returns true (`!serverStatus?.serverHasPassword`) and server-side IsAdmin(c) resolves the injected admin identity.

**FAIL looks like:** Any admin-only control (Auto Downloader nav/page, torrent-client server-wide buttons, extension Preferences gear) is hidden or 403s on a password-less local install — indicates useIsAdmin()'s `!serverStatus?.serverHasPassword` short-circuit was dropped, or serverHasPassword is being reported true incorrectly, or IdentityMiddleware's password-less admin-injection path regressed.

**If it fails, look here first:** seanime-web/src/app/(main)/_hooks/use-server-status.ts useIsAdmin() — confirm the `!serverStatus?.serverHasPassword ||` short-circuit is intact; internal/handlers/identity.go IdentityMiddleware — confirm the local/no-password branch still injects the admin user (dataUserID resolves to admin, CurrentUserRole == UserRoleAdmin).


### T-EXT-R1-BULK-RELOAD-FETCH — Bulk 'reload external extensions' must not hang provider fetch() (THE regression test)

**Covers:** R1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Any running seanime server (local password-less build is fine — this bug has nothing to do with auth). At least one enabled AnimeTorrentProvider or MangaProvider extension installed (any Hibike-style JS extension works). Browser devtools open on the app.

**Preconditions:** IMPORTANT: the code assumes a 'Settings -> Extensions -> Reload extensions' UI button exists, but it does not: grep confirms POST /api/v1/extensions/external/reload (HandleReloadExternalExtensions, internal/handlers/extensions.go:161) has NO frontend call site anywhere in seanime-web/src — useReloadExternalExtensions (extensions.hooks.ts:132) is defined but never invoked by any component. The extensions page (extension-list.tsx) only has a per-extension reload icon (ExtensionCard's allowReload prop, extension-card.tsx:373-388), which calls the SAFE single-extension path (HandleReloadExternalExtension -> ReloadExternalExtension(id) -> reloadExtension, external.go:844, which already called DeletePluginPool before this fix and is NOT what R1 is about). To exercise the BULK path that R1 actually fixed, you must call the endpoint directly from the browser console while on the seanime tab (reuses the session's cookies): fetch('/api/v1/extensions/external/reload', {method:'POST'}).then(r=>r.json()).then(console.log)

**Steps:**

1. Open the anime entry page for any show with torrents (or the manga reader for a manga provider), and open the torrent search drawer (torrent-search-button.tsx -> torrent-search-drawer.tsx) or manga chapter list. Run one search successfully first to confirm the provider works pre-reload. 2. Open browser devtools console on the same origin and run: fetch('/api/v1/extensions/external/reload', {method:'POST'}).then(r=>r.json()).then(console.log) -- wait for it to resolve (it will return {data:true}; the reload itself is synchronous server-side inside the handler). 3. Immediately re-run the same torrent/manga search from step 1 (close and reopen the search drawer, or click 'Search' again) without reloading the page.

**PASS looks like:** The search returns results within its normal time (a couple seconds), exactly like step 1. No hang.

**FAIL looks like:** The search spinner/loading state in torrent-search-drawer.tsx or the manga chapter fetch spins FOREVER with no error toast and no network response ever completing in the Network tab (the underlying goja fetch() promise never settles, so the Go handler's WaitForPromise blocks). There is no error string to grep for -- that is the whole danger of this bug: it is a silent hang, not a logged failure. If you want server-side confirmation, tail the log for 'extensions: Killed Goja VMs' (external.go:511, printed once by interruptExternalGojaExtensionVMs) followed by the search request never producing a matching provider log line at all.

**If it fails, look here first:** Check internal/extension_repo/goja_base.go ClearInterrupt (~line 276-289): confirm g.runtimeManager.DeletePluginPool(g.ext.ID) is called BEFORE g.fetches.closeAll(). If the ordering was reverted or DeletePluginPool call removed, GetOrCreatePrivatePool (goja_runtime_manager.go) hands the next provider instance a stale pool whose factory closure is bound to the now-permanently-closed fetchRegistry, so every new Fetch is closed on arrival (fetchRegistry.add) and beginRequest() returns false forever.


### T-EXT-R1-REPEAT — Bulk reload survives repeated invocations (not just the first one)

**Covers:** R1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same as T-EXT-R1-BULK-RELOAD-FETCH.

**Preconditions:** Confirms the fix isn't a one-shot fluke and that DeletePluginPool correctly re-primes the pool's factory closure each time (the audit's severity correction noted this breaks 'on the first bulk reload', so hammering it 3x proves the fix doesn't just paper over a race).

**Steps:**

1. From the browser console, run the reload fetch call 3 times in a row, waiting for each to return before firing the next: for (let i=0;i<3;i++){ await fetch('/api/v1/extensions/external/reload',{method:'POST'}).then(r=>r.json()); } 2. After the loop finishes, run the same torrent/manga search as in T-EXT-R1-BULK-RELOAD-FETCH.

**PASS looks like:** Search still returns results normally after 3 consecutive reloads.

**FAIL looks like:** Same silent-hang symptom as T-EXT-R1-BULK-RELOAD-FETCH, but appearing only after the 2nd or 3rd reload instead of the 1st -- would indicate DeletePluginPool itself is leaving state behind (e.g. GetOrCreatePrivatePool racing a not-yet-cleaned pool map entry) rather than the ordering fix being wrong.

**If it fails, look here first:** Check internal/goja/goja_runtime/goja_runtime_manager.go DeletePluginPool (line 59) actually removes the entry from m.pluginPools (not just interrupts runtimes inside it) so the next GetOrCreatePrivatePool call takes the ok==false branch and runs the new initFn/fetchRegistry.


### T-DBG-1-REGRESSION — Continue-Watching / Next-Episode preloaded playback still plays normally (regression direction)

**Covers:** DBG-1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Denshi INSTALLED build (scripts/build-denshi-local.sh --installer — unpacked builds have known playback bugs per mpv-prism-overlay-constraint memory, and this is a player-affecting fix so browser can't test it). Since the fix is uncommitted/undeployed, run the Go server locally with the working tree as-is (`go run .` from repo root — banner prints the bind port, default 43211 per internal/core/config.go:101) and point Denshi at it: Settings > External Server URL = http://127.0.0.1:43211 (see denshi-external-server memory). TorBox configured as the active provider in Settings > Debrid Service (DebridSettings, seanime-web/src/app/(main)/settings/_containers/debrid-settings.tsx:31) with a valid API key.

**Preconditions:** At least one anime sits in the home page's Continue Watching row (continue-watching.tsx) with 1+ unwatched episode; prewarm/preload is enabled (default). Tail the server's stdout/log for the strings quoted below.

**Steps:**

1. Start the local server and confirm it's listening (banner shows port 43211). 2. Point Denshi's Settings > External Server URL at http://127.0.0.1:43211 and reconnect. 3. On Home, wait ~10-20s for the Continue Watching row to prewarm in the background. 4. Click a Continue Watching episode card to resume playback. 5. Tail the server log for `debridstream: Using preloaded stream for episode <ep>` (stream.go:1539) — confirms the preload path (not cold-resolve) was actually exercised. 6. Let the episode play past ~20-30s (longer than the historically-observed 12s truncation window). 7. In the player's control bar, click the Next button (VideoCoreNextButton, video-core-control-bar.tsx) to trigger prewarmed next-episode playback and repeat step 6 for it.

**PASS looks like:** Both plays start and run continuously past 20-30s with no error toast; the DebridStreamState pill goes Downloading -> Ready with message "Ready to stream the file" (stream.go:1730); the log shows `Using preloaded stream for episode ...` and NEVER shows `directstream(http): CDN Content-Length disagrees with the provider's file size; aborting open (truncated/rotten cached copy)`.

**FAIL looks like:** An error toast appears within seconds, reading "debrid CDN served a truncated file: X of Y bytes (Z.ZZ%) — the torrent's cached copy is incomplete on the provider; re-add it or pick another release" (httpstream.go:154, surfaced via HTTP 500 from POST /api/v1/debrid/stream/start -> RespondWithError -> toast.error in seanime-web/src/api/client/requests.ts:235) even though served/expected are actually equal or the mismatch is spurious; OR a generic "Failed to play preloaded stream" server-log Error (stream.go:1722) with no toast detail.

**If it fails, look here first:** s.knownFileSizeFor(torrentItemId, cached.fileId) at stream.go:1716 returned a stale/wrong size for the URL actually being served. Check first whether the `torrentItemId` snapshotted under preloadMu at stream.go:1584 is out of sync with the entry that was refreshed inside the block above it (stream.go:1621-1659) — a URL refresh updates `cached.streamUrl`/`cached.urlResolvedAt` but the local `torrentItemId` var isn't re-derived from `cached` afterward.


### T-PLAYLIST-1 — Playlist end-to-end lifecycle: multi-episode, next, STOP mid-episode, failed episode start

**Covers:** PLAYLIST-1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Any Seanime build (browser is enough, no Denshi needed). Local library with >=2 downloaded/streamable episodes of the same anime for a normal playlist, plus one episode you can force to fail (e.g. point the entry at a moved/deleted local file, or a torrent-stream episode with no seeds/dead magnet so playEpisode's torrentstream branch errors).

**Preconditions:** Server running (deploy-server.sh not required — this is pure playback logic, testable against current dev build or already-deployed build). Library page open, 'Playlists' feature available (seanime-web/src/app/(main)/_features/playlists).

**Steps:**

1. Open the Playlists modal (library page > Playlists), build a playlist with 3 episodes (ep A, ep B = the one you rigged to fail, ep C), and hit Play. 2. Watch ep A play to completion (or fast-forward near the end) and confirm the manager auto-advances: playNextEpisode fires, ep B is loaded. 3. Since ep B is rigged to fail to start, confirm playEpisode's error path (m.logger.Error 'playlist: Failed to start playing local file' or 'playlist: Failed to start playing nakama stream') fires and the playlist STOPS cleanly (toast 'Failed to start playing...' appears, UI returns to idle/library) rather than hanging. 4. Restart the playlist from ep A, and this time manually click Stop mid-episode (before it completes). Confirm playback stops immediately and the UI returns to idle. 5. Immediately after step 4, start the SAME or a different playlist again and confirm it starts and plays normally (state wasn't left corrupted by the STOP).

**PASS looks like:** Every transition (auto-advance, forced-error stop, manual stop, restart) completes within a couple seconds. The UI never freezes/spins, the playlist state resets to idle after each stop, and a fresh playlist always starts cleanly afterward.

**FAIL looks like:** UI hangs after the failing episode or after Stop -- no toast, no state change, Play/Stop buttons unresponsive, and the playlist websocket (ClientEventCurrentPlaylist) stops emitting updates. Server-side this is a real goroutine deadlock: the fixed code takes m.mu in playEpisode's error branch via stopPlaylistLocked; if that call is ever reached from a path that already-double-locks (a regression of the manager.go split), the whole Manager blocks and even unrelated playlist websocket events (ClientEventCurrentPlaylist) never respond again for the rest of the process. Grep server log for the absence of 'playlist: Stopping current playlist' after the failure log line -- if the error log appears but the stop log never follows, it deadlocked in stopPlaylistLocked.

**If it fails, look here first:** internal/playlist/manager.go playEpisode (~line 660-696): confirm all three error branches call m.stopPlaylistLocked(...) not m.StopPlaylist(...) -- the latter re-takes m.mu and self-deadlocks since playEpisode's callers already hold it. Also check stopPlaylistLocked (~line 771) doesn't call anything that re-locks m.mu (e.g. accidentally calling the public StopPlaylist or resetPlaylist instead of resetPlaylistLocked).


### T-PLAYLIST-2 — Rapid playlist restart does not kill the new session (sessionGen regression check)

**Covers:** PLAYLIST-1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same as T-PLAYLIST-1, browser only.

**Preconditions:** A playlist of >=2 episodes ready to play.

**Steps:**

1. Start the playlist and let it begin playing ep 1. 2. Within a second or two (before the episode meaningfully progresses), hit Stop, then immediately hit Play on a playlist again (same or different one) -- the goal is to fire ClientEventStart while the FIRST session's teardown goroutine (the one selecting on sessionCtx.Done()) may still be running. Repeat this stop/immediately-restart cycle 3-4 times in quick succession. 3. On the final restart, let the playlist play through normally (auto-advance to episode 2, or manually click next episode).

**PASS looks like:** Every restart produces a live, responsive playlist: the currently-playing episode indicator updates, playback status events keep flowing (progress bar moves), and next-episode/auto-advance keeps working on the LAST session you started. No 'ghost' teardown from an earlier stopped session interferes.

**FAIL looks like:** After a rapid stop+restart, the NEW playlist silently stops responding to playback events (progress bar frozen, no auto-advance, no 'Current playlist' websocket updates) even though the server shows no error -- this is the exact bug the fix's sessionGen field exists to prevent: the OLD (superseded) session goroutine's ctx.Done() branch would call m.playbackManager.UnsubscribeFromPlaybackStatus('playlist-manager') / m.mediacoreCoordinator.Unsubscribe('playlist-manager'), which are process-wide keys shared with the NEW session, silently killing the new session's event channel. Look for server log 'playlist: Superseded playlist session goroutine exiting' (Trace) -- if you see 'playlist: Current playlist context done' immediately followed by an Unsubscribe from an old session AFTER a new session already started, the guard regressed.

**If it fails, look here first:** internal/playlist/manager.go, the ctx.Done() branch (~line 322-345): confirm `superseded := m.sessionGen != sessionGen` is checked BEFORE calling UnsubscribeFromPlaybackStatus/mediacoreCoordinator.Unsubscribe, and that the goroutine `return`s immediately when superseded=true. Also confirm startPlaylist increments m.sessionGen under m.mu before spawning the new goroutine (~line 304).


### T-MC-SESSION-EVICT — AniList disconnect/reconnect (session eviction) does not kill live playback signalling or progress sync -- the silent-failure case

**Covers:** MC-2, MC-3, DB-1  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Networked/password-protected server (local password-less install makes everyone admin and skips this teardown path entirely -- adminSession() has a nil platformRef). A REGULAR (non-admin) user account with AniList linked, on Settings > Connected Accounts. This can be tested against the Pi deployment after redeploy, or a local server started with a password set.

**Preconditions:** Log in as the regular (non-admin) user. Confirm Settings shows 'Connected as <AniList username>' in the AniList integration card (seanime-web/src/app/(main)/settings/_containers/integrations-settings.tsx).

**Steps:**

1. As the regular user, play any episode to completion (or use whatever manual progress-update action exists) so a normal AniList progress sync fires. Confirm the episode's progress updated on your AniList list (or in Seanime's own list view after a refetch). Server log should show no 'mediacore: Failed to update progress for media %d' error for that mediaId. 2. On the Settings page, click 'Disconnect' next to AniList (confirm the dialog), then immediately click 'Connect with AniList' and relink the SAME account. This round-trip fires HandleLogout -> LogoutUserFromAnilist -> evictSession(userID) -> shutdown() (tears down mediaCoord/videoCore/platformRef), then HandleLogin -> LoginUserToAnilist -> evictSession(userID) again -> a brand-new session is built on the next request. 3. Watch the server log through this disconnect/reconnect -- confirm there is NO panic, NO hang (the HTTP request for /auth/login must return normally, not time out), and you see 'app: User authenticated to AniList' after reconnecting. 4. Immediately after reconnecting, start playing a DIFFERENT episode. Confirm the loading screen actually resolves to playback (this is the mediacore Coordinator signal path -- if the rebuilt session's Coordinator/effects goroutine were dead, the player would hang on 'Starting video'/'Signaling player...' the way the 57ad877d regression did). 5. Let that episode reach completion (or trigger the manual progress action again) and confirm progress syncs to AniList AGAIN -- this proves the rebuilt session's CacheLayer ticker (DB-1) and Coordinator effects goroutine (MC-2) are alive, not silently dead.

**PASS looks like:** Disconnect+reconnect completes in normal request time with no hang; a subsequent episode plays normally (loading screen resolves); a subsequent progress update reaches AniList exactly like step 1 did.

**FAIL looks like:** This fails SILENTLY -- there is no error toast or crash. The tell is: after reconnecting, playback either never leaves the loading screen (Coordinator dead -- MC-2 regressed, mirrors the 57ad877d bug where 'Signaling player...' prints but the signal never lands) OR playback works but the episode's progress never updates on AniList after step 5 even though step 1 worked (CacheLayer ticker dead too early or wired to the wrong/stale instance -- DB-1 regressed). Grep the log for 'anilist platform: Updating entry progress' (Trace, always logged when UpdateEntryProgress is called) followed by the ABSENCE of any subsequent AniList list change -- and check there's no 'mediacore: Failed to update progress for media %d' error either (that would be a loud failure, not the silent one this test targets).

**If it fails, look here first:** internal/core/session.go shutdown() (~line 564-591): confirm the platformRef teardown uses the raw `s.platformRef` field (nil-guarded) and NOT `s.PlatformRef()` (which returns the App-global platform for admin sessions and would be a different, worse bug). internal/platforms/shared_platform/cachelayer.go Close()/cachelayer_queue.go startQueuedUpdateSync() (~line 42-52): confirm the ticker loop selects on `c.stop` and that NewCacheLayer's `stop` channel is fresh per instance (a shared/reused channel across sessions would close the NEW session's ticker too). internal/mediacore/mediacore.go SetupSharedEffects (~line 466-480): confirm the select is on `c.stopCh` (per-Coordinator, set fresh in New()) not a package-level or shared channel.


### T-NAK-1 — Watch-party host/peer session churn must not crash the server (NAK-5 map-marshal race + NAK-3 participant-field race)

**Covers:** NAK-5, NAK-3  |  **Priority:** P0-blocker  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** TWO separate running Seanime SERVER processes (not two accounts on one server, and not Denshi). This is the cross-instance Nakama peer/host link in Settings > Nakama, backed by internal/nakama/watch_party_host.go — a DIFFERENT subsystem from the same-server multi-account 'Watch Rooms' feature covered in T-EVT-1. IMPORTANT, confirmed by reading the code: the frontend button UI for this feature was removed. seanime-web/src/app/(main)/_features/nakama/nakama-manager.tsx:551 has the literal comment 'Watch Party (legacy peer/host) removed — Watch Rooms (top) replaces it.' The useNakamaCreateWatchParty/useNakamaJoinWatchParty hooks (seanime-web/src/api/hooks/nakama.hooks.ts:73-104) have zero callers anywhere in the UI. The backend endpoints are still fully live, so drive them with curl.

**Preconditions:** Instance A: Settings > Nakama > 'Enable Nakama' switch on, then the 'Hosting' tab > 'Enable host mode' + a Passcode set (internal/handlers/nakama.go HandleNakamaCreateWatchParty requires Settings.GetNakama().IsHost==true). Confirm the 'Currently hosting' badge (nakama-settings.tsx:84-85) appears. Instance B: Settings > Nakama > 'Enable Nakama' on, 'Connect as a Peer' tab > 'Nakama Server URL' = http://<A-host>:<A-port>, 'Nakama Passcode' = A's passcode, Save. Confirm B's Nakama panel shows a 'Host connection' card (nakama-manager.tsx:528-549) and no NAKAMA_ERROR toast.

**Steps:**

1. On A, create a session: curl -X POST http://<A>/api/v1/nakama/watch-party/create -H "Content-Type: application/json" -d '{"settings":{"syncThreshold":2.0,"maxBufferWaitTime":10}}'  -> expect {"data":true}.
2. On B, join it: curl -X POST http://<B>/api/v1/nakama/watch-party/join -H "Content-Type: application/json" -d '{"clientId":"manual-test-peer-1"}'  -> expect {"data":true}. (This populates session.Participants on A via WatchPartyManager.JoinWatchParty.)
3. Generate churn: repeatedly stop/restart Instance B's process (or flip its network) 10-15 times over a few minutes, re-issuing the join curl from step 2 each time it comes back. Every join/leave inserts/deletes from session.Participants (handleWatchPartyPeerJoinedEvent/handleWatchPartyPeerLeftEvent) while any live peer's periodic status/buffer reports concurrently call sendSessionStateToClient, which marshals that same map (handleWatchPartyPeerStatusEvent/handleWatchPartyBufferUpdateEvent, internal/nakama/watch_party_host.go:637-716).
4. Watch Instance A's console/log continuously throughout step 3.

**PASS looks like:** Instance A stays up through all the join/leave churn; every curl call returns a normal JSON response (200 data, or an ordinary error JSON like {"error":"not connected to host"} for a stale request) — never a dropped connection or timeout caused by the server dying.

**FAIL looks like:** Instance A's process exits outright: `fatal error: concurrent map iteration and map write` printed to stderr/log with a goroutine trace referencing sendSessionStateToClient/WatchPartySession, then the process is gone. This is a Go runtime fatal, not a recoverable panic — no util.HandlePanicInModule line precedes it. If A runs as systemd seanime.service, `journalctl -u seanime -n 100 --no-pager` shows the fatal line plus a restart gap at that timestamp.

**If it fails, look here first:** internal/nakama/watch_party_host.go sendSessionStateToClient (~line 504-533): confirm session.mu.RLock() -> snapshot-copy of Participants -> session.mu.RUnlock() happens BEFORE wpm.manager.wsEventManager.SendEvent(...), not after or removed.


### T-SYNC-1 — Polling the local-sync queue endpoint during an active sync must not crash the server (SYNC-1 map race, SYNC-2 lock)

**Covers:** SYNC-1, SYNC-2  |  **Priority:** P0-blocker  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Single local Seanime instance, any authenticated (non-simulated/non-offline-guest) account — the /sync page (seanime-web/src/app/(main)/sync/page.tsx) returns 'Not authenticated' for serverStatus.user.isSimulated. No debrid/plugin/Denshi needed. A terminal capable of a curl loop (Git Bash) or the browser devtools console on the /sync tab.

**Preconditions:** At least 2-3 anime tracked for offline sync so a real sync job gets queued (use the '+' / SyncAddMediaModal on /sync to track a few titles if the list is empty) — a wider job set widens the race window across the two background goroutines (processAnimeJobs, processMangaJobs in internal/local/sync.go).

**Steps:**

1. Go to /sync. Track 2-3 anime for offline use if none are tracked yet.
2. Have a request-flood ready against GET /api/v1/local/queue, e.g. in the browser console on the /sync tab: `for(let i=0;i<500;i++) fetch('/api/v1/local/queue')`, or a Git Bash loop: `for i in $(seq 1 500); do curl -s -o /dev/null http://<host>:<port>/api/v1/local/queue; done`.
3. Click 'Sync now' -> 'Update local data' (handleSyncLocal / useLocalSyncData) to start processAnimeJobs/processMangaJobs, which is when the writer goroutines actually mutate queueState.AnimeTasks/MangaTasks (assign then delete per task).
4. Immediately (within 1-2 seconds) fire the request flood from step 2 so GETs land while the writer goroutines are actively assigning/deleting map entries.
5. Repeat steps 3-4 a couple of times (re-track more media, sync again) — this is timing-dependent and may not reproduce on the first attempt.

**PASS looks like:** Server stays up throughout; every GET /api/v1/local/queue call returns 200 with a JSON {animeTasks, mangaTasks} snapshot; the sync completes and the 'Sync now' spinner clears normally.

**FAIL looks like:** The entire Seanime server process exits — every open tab loses its websocket/API connection at once, with no HTTP 500 or client-visible error (the crash happens server-side before any response is written). The exact runtime line, if you're watching server stdout/log or `journalctl -u seanime`, is `fatal error: concurrent map read and map write` with a goroutine trace naming GetQueueState / processAnimeJobs / processMangaJobs. This is unrecoverable — no defer/recover catches it.

**If it fails, look here first:** internal/local/sync.go GetQueueState (~line 170-186): confirm it still takes q.queueStateMu.RLock() and returns freshly-allocated copied maps (not q.queueState by value, which shares the underlying map headers). Also check the four SendQueueStateToClient() call sites in processAnimeJobs/processMangaJobs (~sync.go:102,112,129,139) weren't moved back to running BEFORE queueStateMu.Unlock() — since SendQueueStateToClient calls GetQueueState, an in-lock call there self-deadlocks (a permanent 'Sync now' spinner that never clears is the OTHER regression to watch for, not just a crash).


### T-AUTH-5 — Extension user-config: non-admin has no visible entry point and is 403'd server-side; admin still can view/edit

**Covers:** AUTH-4  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Networked server with password set. Two accounts. Requires at least one installed extension that declares a userConfig (check /extensions page — any extension whose card shows the gear/Preferences icon, e.g. a torrent-provider or MAL/AniList-adjacent extension with configurable fields).

**Preconditions:** At least one extension with `extension.userConfig` truthy installed (its card shows a 'Preferences' gear IconButton).

**Steps:**

1. Log in as admin, go to /extensions. Confirm the gear 'Preferences' icon is visible on the extension card; open it, change a value, save, confirm success (POST /api/v1/extensions/user-config succeeds). 2. Log out, log in as the non-admin user. Go to /extensions and find the SAME extension card. Confirm the 'Preferences' gear icon is ABSENT entirely (ExtensionUserConfigModal returns null for non-admin, so even the trigger button is not rendered — not just disabled). 3. Directly call GET /api/v1/extensions/user-config/{extId} with the non-admin's session. Expect 403. 4. Directly POST /api/v1/extensions/user-config with a valid body as the non-admin. Expect 403.

**PASS looks like:** Non-admin never sees the Preferences control on any extension card; both GET and POST /extensions/user-config 403 for the non-admin with 'admin privileges required' (GET) / the privileged-extension-management error (POST). Admin's view/edit flow is unaffected.

**FAIL looks like:** The gear icon is visible (even if clicking it 403s — that's the 'silent 403 from a visible control' failure mode this fix is meant to prevent) for the non-admin; or GET/POST return 200 for the non-admin; or the admin's own Preferences save now fails (guardPrivilegedExtensionManagement wrongly rejecting an admin, e.g. because IsStrict()+trusted-local check misfires for a remote admin — see the AUTH-4 GET deviation note about not using the full privileged guard on GET).

**If it fails, look here first:** seanime-web/src/app/(main)/extensions/_containers/extension-user-config.tsx line ~35 `if (!isAdmin) return null` (must be before the Modal/trigger render, not after); internal/handlers/routes.go `v1Extensions.GET("/user-config/:id", h.HandleGetExtensionUserConfig, h.AdminOnly)`; internal/handlers/extensions.go HandleSaveExtensionUserConfig — check `h.guardPrivilegedExtensionManagement(c)` call precedes the config save; internal/extension_repo/userconfig.go SaveExtensionUserConfig — check `isValidExtensionIDString(id)` gate is still present (id becomes the filecache bucket key).


### T-EXT-PLG4-GOROUTINE-LEAK — Plugin unload actually reclaims its fetch pump goroutines (the original PLG-4 leak)

**Covers:** PLG-4  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Any running seanime server with at least one installed Plugin-type extension (Extensions page 'Plugins' card, extension-list.tsx:277-294) that uses network fetch (most plugins bind fetch via goja_plugin.go's pool factory + uiVM).

**Preconditions:** Baseline the goroutine count via GET /api/v1/memory/stats (internal/handlers/status.go:537, field numGoroutine) or download a stack dump via GET /api/v1/memory/goroutine (pprof profile, status.go:640-661) for a before/after diff. Neither route requires extra setup beyond the server running.

**Steps:**

1. In the browser or via curl, GET http://<server>/api/v1/memory/stats and note numGoroutine (call it N0). 2. On the Extensions page, open the plugin's settings modal (the ... icon -> ExtensionSettings) and click 'Disable', wait ~1s, then 'Enable' again -- this triggers SetExternalExtensionDisabled which unloads/reloads the plugin (goja_plugin.go ClearInterrupt path, which is the ORIGINAL PLG-4 fix target: DeletePluginPool + p.fetches.closeAll()). Repeat this disable/enable cycle 5 times. 3. Wait 2-3s for goroutines to actually exit their range loop after Close(), then GET /api/v1/memory/stats again and note numGoroutine (N1).

**PASS looks like:** N1 is roughly equal to N0 (within normal jitter, +/- a few for unrelated background work) -- NOT growing by ~6 per cycle (5 pool-prewarmed runtimes + uiVM, per the audit's count at goja_runtime_manager.go newPool(5,...)).

**FAIL looks like:** numGoroutine climbs by roughly a constant increment (~6, or a multiple of it) each disable/enable cycle and never comes back down. Confirm via GET /api/v1/memory/goroutine pprof dump: look for stack frames stuck in goja_bindings.BindFetch's pump goroutine, i.e. `for fn := range f.ResponseChannel()` at internal/goja/goja_bindings/fetch.go (the anonymous func spawned in BindFetch), blocked on a channel receive.

**If it fails, look here first:** Check that GojaPlugin.ClearInterrupt (goja_plugin.go ~line 111-118) still calls p.fetches.closeAll() after p.runtimeManager.DeletePluginPool(p.ext.ID), and that p.fetches.add(...) is actually wired at both BindFetch call sites (goja_plugin.go: the pool factory ~line 180 and the uiVM bind ~line 200) rather than the return value being silently discarded again.


### T-EXT-PLG4-INFLIGHT-SETTLES — An in-flight fetch() at unload time still resolves/rejects instead of leaving the pump mid-panic-window

**Covers:** PLG-4  |  **Priority:** P1-important  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** Any running seanime server with a plugin or provider extension that makes a slow network fetch (a provider hitting a slow/rate-limited torrent site, or any plugin calling a deliberately slow endpoint). No special build needed.

**Preconditions:** This targets the beginRequest/inflight WaitGroup redesign in fetch.go (Close() now waits for f.inflight before closing vmResponseCh, instead of closing it immediately and risking a send-on-closed panic). Automated coverage: fetch.go has unit tests for Fetch, but the audit's own R2 refutation notes the timeout=0 drain-goroutine-leak scenario was checked in source, not exercised live -- treat the live in-flight-at-close interleave as unverified by tests.

**Steps:**

1. Start a torrent search (or trigger any plugin fetch) against a source you know responds slowly (several seconds). 2. While that request is still in flight (before results appear), reload that single extension via the per-extension reload icon (ExtensionCard's reload button, extension-card.tsx:373-388) or, if testing the provider path, fire the bulk reload console command from T-EXT-R1-BULK-RELOAD-FETCH mid-search. 3. Watch the server log for the duration of the in-flight request's original timeout.

**PASS looks like:** The in-flight request either completes normally and its result is silently dropped (extension already torn down), or the promise settles quickly -- no WARN/panic log line, and the server process keeps running normally. The subsequent search (after reload finishes) still works per T-EXT-R1-BULK-RELOAD-FETCH.

**FAIL looks like:** A 'panic: send on closed channel' recovered-panic WARN in the server log (would surface via util.HandlePanicInModuleThen('goja/goja_bindings/Fetch', ...) at fetch.go, or the pump's own recover logging 'extension: response channel panic: %v'), OR the extension reload call itself hangs for the fetch's full duration instead of returning immediately (would indicate Close() is blocking synchronously on f.inflight.Wait() instead of doing it in the background goroutine).

**If it fails, look here first:** Check fetch.go Fetch.Close(): the f.inflight.Wait(); close(f.vmResponseCh) sequence must run inside `go func(){...}()` (non-blocking), and Fetch() must call f.beginRequest() (which takes inflightMu) BEFORE spawning its request goroutine, not after, so Close can't race a request that started registering but hasn't been counted yet.


### T-DBG-1-PACK — Multi-file pack/batch torrent: two preloaded episodes from the SAME torrentItemId both play (size-cache key collision worry)

**Covers:** DBG-1  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same local-server + Denshi installed-build setup as T-DBG-1-REGRESSION, TorBox active.

**Preconditions:** An anime where a single torrent covers multiple episodes (a batch/pack release, manually picked via the debrid file picker so BatchEpisodeFiles applies), OR two episodes whose files come from the same torrentItemId.

**Steps:**

1. Open the anime and manually add/select a batch torrent covering 2+ episodes via the debrid picker. 2. In the player's in-line episode playlist, hover two different episode cards from that same torrent to trigger preload for each (PlaylistEpisodeHoverCard, video-core-playlist.tsx:403 — background preload fires on hover). 3. Play episode A; confirm normal playback (per T-DBG-1-REGRESSION's expected/failure signatures). 4. Play episode B (different file, same torrentItemId); confirm normal playback.

**PASS looks like:** Both episodes play fully with no truncation abort; log shows two separate `Using preloaded stream for episode ...` lines, one per episode, neither followed by the truncation-abort line.

**FAIL looks like:** One of the two episodes (commonly whichever is played second) fails to open with the "debrid CDN served a truncated file: ..." toast, while playing the SAME file cold (fresh, not via Continue Watching) works fine — proving the preload path handed it the wrong expected size.

**If it fails, look here first:** TorBox's fileIdCache is keyed "torrentID|shortName" (torbox.go storeFileId/loadFileId, ~line 173-188), not by fileID — check whether the two episodes' files produced colliding cache keys (identical torrentID + shortName), or whether `cached.fileId` read at stream.go:1716 doesn't match the fileId that actually resolved `streamUrl` for that entry.


### T-DBG-1-NONTORBOX — Non-TorBox provider (RealDebrid/Premiumize) preloaded playback is completely unaffected by this change

**Covers:** DBG-1  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same local-server + Denshi setup, but with RealDebrid or Premiumize set as the active provider in Settings > Debrid Service instead of TorBox — only internal/debrid/torbox/torbox.go:198 implements KnownFileSize/debrid.FileSizeKnower; no other provider file (realdebrid.go, premiumize.go) defines it, so `knownFileSize`'s type-assertion at stream.go:388 fails for them and returns 0.

**Preconditions:** Valid RealDebrid or Premiumize account linked; Continue Watching row populated as in T-DBG-1-REGRESSION.

**Steps:**

Repeat T-DBG-1-REGRESSION steps 3-7 verbatim with RealDebrid/Premiumize as the active provider.

**PASS looks like:** Identical behavior to before this diff: playback proceeds normally; the truncation guard never trips for this provider under any circumstance, because ExpectedSize is always 0 on this path (knownFileSize returns 0 for any provider that isn't a FileSizeKnower).

**FAIL looks like:** Any truncation-abort toast/log line appears for a RealDebrid/Premiumize preloaded stream. Since ExpectedSize can only be non-zero via the FileSizeKnower type assertion, this would mean something outside this diff changed (e.g. a provider struct picked up a stray KnownFileSize method) — worth ruling out but not expected from stream.go's diff itself.

**If it fails, look here first:** Check internal/debrid/debrid/debrid.go's FileSizeKnower interface and grep provider packages for any new KnownFileSize implementation outside torbox.go — this diff itself cannot cause a false trip here (knownFileSizeFor fails open to 0 whenever the type assertion at stream.go:388 fails).


### T-TS-1 — Torrentstream start/stop/restart cycling and shutdown-mid-stream stability

**Covers:** TS-1  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Browser or Denshi, a torrent-stream-capable anime entry with a healthy torrent source. Server must be one you can restart (local dev build or a non-prod instance -- do NOT restart prod mid-test since this kills real streams; use a local `go run` instance or a Denshi-pointed local server).

**Preconditions:** Torrent-streaming enabled and working.

**Steps:**

1. Start a torrent stream for episode 1, let it play a few seconds (past the point where StartStream sets currentFile/currentTorrent and calls ResetBaselines), confirm the player shows normal buffering/progress stats. 2. Stop the stream (leave the player / stop). Confirm the log shows 'torrentstream: Stopping stream' then 'torrentstream: Stream stopped'. 3. Immediately start a DIFFERENT episode's torrent stream. Confirm it starts cleanly and stats (speed, percentage, peers) reflect the NEW torrent, not stale data from the first one. Repeat start/stop 3-4 times rapidly. 4. While a stream is actively playing, restart/kill the server process (`Ctrl+C` on `go run`, or `systemctl stop` on a non-prod instance). Confirm the process exits without a panic/stack trace in the log (specifically no nil-pointer panic from a torrent/file mo.Option), and cleanup log lines appear ('torrentstream: Closing torrent client').

**PASS looks like:** Every stream start shows correct, current-torrent stats (not the previous torrent's numbers); no crash on shutdown while streaming.

**FAIL looks like:** A crash/panic in the server log naming torrentstream/client.go's status goroutine (a nil-deref via .MustGet() on currentTorrent/currentFile), OR the in-player stats (download speed / % complete) visibly showing the PREVIOUS episode's numbers for a moment after starting a new one, OR the server hanging (not exiting) on Ctrl+C/systemctl stop while a stream is active. This is a data race so it will NOT reproduce every time -- the fix narrowed the exposure window to <1s around StartStream's write, so this is a best-effort stability smoke test, not a guaranteed repro.

**If it fails, look here first:** internal/torrentstream/stream.go ~line 292-298 (StartStream): confirm r.client.currentFile and r.client.currentTorrent are both written between one r.client.mu.Lock()/Unlock() pair, and internal/torrentstream/client.go ~line 495-499 (Shutdown): confirm the same two fields are written under c.mu.Lock() there too. If either write reverted to unlocked, the background status goroutine (client.go ~line 177) racing it is the source of stale/torn stats.


### T-VC-REGRESSION — Live (non-evicted) browser playback: subtitle upload + InSight characters still work after the effects/insight goroutine rewrite

**Covers:** MC-3  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Browser, any account, no eviction involved -- this is the happy-path regression check for the effects.go/insight.go for-range-to-for-select rewrite.

**Preconditions:** An online-stream or web-player (torrentstream in-browser, not Denshi) episode actively playing, since videocore's effects/insight subscribers only fire for IsOnlinestream()/IsWebPlayer() events.

**Steps:**

1. Start playing an episode via the web player (online-stream tab, or a torrent-stream episode played in-browser without Denshi). 2. Drag-and-drop a .srt or .ass subtitle file directly onto the video player. Confirm the 'Adding subtitle file...' toast appears and a new subtitle track shows up in the track selector shortly after (this exercises setupOnlinestreamEffects' SubtitleFileUploadedEvent handler in effects.go). 3. Open the InSight panel (character lookup panel in the web player) for the currently playing anime. Confirm the Characters list populates (this exercises insight.go's VideoLoadedEvent handler -> fetchCharacters -> sendToPlayer). 4. Stop playback normally (not eviction) and start a second episode; repeat step 3 to confirm InSight still works on the second load (VideoTerminatedEvent -> stopPolling/Clear -> next VideoLoadedEvent should still be delivered).

**PASS looks like:** Both the subtitle upload and the InSight character panel work exactly as before on every episode load within the SAME session -- the rewrite from `for e := range subscriber.Events()` to `select { case <-dispatcherStop: ...; case e, ok := <-subscriber.Events(): ... }` must not change delivery of normal (non-shutdown) events.

**FAIL looks like:** Dropped subtitle file produces the 'Adding subtitle file...' toast but no track ever appears (effects.go's select is mis-wired so events stop being consumed after the first one), or the InSight Characters panel stays empty/spinner-forever on the second episode load (insight.go's select exits early or the `ok` check on a still-open channel incorrectly returns). Either symptom with NO error toast and NO server-side error log is the regression signature -- events are just silently not being delivered anymore.

**If it fails, look here first:** internal/videocore/effects.go setupOnlinestreamEffects (~line 20-35) and internal/videocore/insight.go Start() (~line 122-135): confirm the `select` only returns on `<-vc.dispatcherStop` or `ok==false` from the events channel, and that neither branch is reached during normal steady-state event flow (i.e. dispatcherStop isn't accidentally closed before Shutdown(), and the `ok` check isn't inverted).


### T-EVT-1 — Closing one browser tab must not silently kill another live tab's websocket events (EVT-2 wrong-victim eviction, regression guard)

**Covers:** EVT-2, EVT-1  |  **Priority:** P1-important  |  **Only verification that exists:** no (automated coverage also exists)

**Environment:** Single local or networked Seanime instance, any one account, two browser tabs/windows pointed at the same server URL (clientId is shared across tabs of the same browser via localStorage — this is exactly the scenario the fix targets). No Denshi/plugin/debrid needed.

**Preconditions:** Two tabs open and both showing a live UI (e.g. both on /sync or the library page) so a server-pushed event is visually observable in both at once.

**Steps:**

1. With Tab 1 and Tab 2 both open on the same account, trigger a global broadcast from Tab 1: start a library scan (same trigger as T-SCAN-1). Confirm the scan progress bar/status updates LIVE in BOTH tabs simultaneously (EventScanProgress/EventScanStatus fan out to every entry in WSEventManager.Conns, not per-tab).
2. Mid-scan (or right after), close Tab 1 entirely (click the browser tab's close button, not just navigate away).
3. In Tab 2 (still open), trigger another broadcast — start a second scan, or watch any other server-pushed update (toast, queue state) — and confirm Tab 2 keeps receiving it with no reload needed.
4. Secondary check (room cleanup, exercises the same GetClientIds()/m.Conns liveness path): in Tab 2, open the Nakama panel -> 'Watch Rooms' section -> 'Create room' -> name it -> 'Create & join', then immediately 'Close room'. Reopen the panel and confirm the room is gone from the list (no lingering ghost room).

**PASS looks like:** Tab 2 keeps receiving every subsequent server-pushed event uninterrupted after Tab 1 closes. The room created-then-closed in step 4 does not linger in the 'Watch Rooms' list.

**FAIL looks like:** Tab 2 silently stops receiving ANY server-pushed event right after Tab 1 closes — the scan bar in Tab 2 freezes mid-scan (or the second scan in step 3 never shows in Tab 2), with no error in Tab 2's console and its network tab still showing the websocket connection as open (this is the tell: a live-looking connection that's been silently dropped from the server's fan-out list). Server-side, internal/handlers/websocket.go logs one 'ws: Client disconnection' for Tab 1's close, but the WRONG *WSConn was removed.

**If it fails, look here first:** internal/events/websocket.go RemoveConn (~line 320-345): confirm it matches by pointer identity (`conn == c`), not `conn.ID == id`. internal/handlers/websocket.go webSocketEventHandler (~line 63-92): confirm it captures `wsConn := h.App.WSEventManager.AddConn(...)` and passes that exact value to `RemoveConn(wsConn)` on disconnect, not the plain client-id string.


### T-MANGA-1 — Long remote-provider chapter in Double Page mode: correct pairing + no unbounded fetch storm

**Covers:** MEDI-NAK-2 (5-slot semaphore on getPageDimensions), MEDI-NAK-3 (race-free per-goroutine error via local ferr/b instead of the shared named return err)  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Any running seanime server (local or Pi) with a remote manga provider configured (e.g. Comick/MangaDex). No Denshi/Pi/multi-account needed.

**Preconditions:** Pick a manga with a chapter that has 150-250+ pages (a long one-shot/artbook-style chapter, or any chapter with a high page count). The chapter must NOT have been opened before in Double Page mode on this server instance, or if it has, its page-dimensions bucket key is `{provider}${mediaId}${chapterId}` in the file cache (internal/manga/chapter_page_container.go getFcProviderBucket) - re-hydration still runs getPageDimensions on every open per chapter_page_container.go:101-103, so a fresh chapter isn't strictly required, but pick one you haven't stress-tested before to avoid any leftover cached PageDimensions from a prior broken run.

**Steps:**

1. Open the manga entry, open the long chapter's reader.
2. In the chapter reader top bar, open reader settings (gear/book icon) and switch Reading Mode to 'Double Page' (seanime-web/src/app/(main)/manga/_containers/chapter-reader/chapter-reader-settings.tsx - the option with the book-page icon labeled 'Double Page').
3. Let the chapter fully load (server call is GetMangaPageContainer -> getPageDimensions with doublePage=true, chapter_page_container.go:204-249).
4. Scroll/page through the ENTIRE chapter end to end, watching how pages are paired (two narrow pages together, wide/splash pages shown alone).
5. Reload the chapter (navigate away and back, or hard refresh) 2-3 times to catch any nondeterministic pairing from a race.

**PASS looks like:** Pages load in correct reading order throughout. Double-page pairing looks visually sane: wide splash/spread pages are shown alone, narrow pages are paired two-up, and pairing is IDENTICAL across repeated reloads of the same chapter (no flicker between 'this page is alone' and 'this page is paired' on repeat loads). No more than 5 page-image requests should be in flight from the server to the remote provider at once (the fix adds `sem := make(chan struct{}, 5)` gating only pages that need an HTTP fetch, i.e. `page.Buf == nil`).

**FAIL looks like:** A page (usually a wide splash/spread) is incorrectly paired with its neighbor instead of shown alone, OR pairing changes between reloads of the same chapter. Root cause if broken: some page's dimension fetch silently failed (goroutine `return`s after `manga_providers.GetImageByProxy` error) so `pageContainer.pageDimensions` is missing that page's index; the client's handle-chapter-reader.ts:319 (`pageContainer.pageDimensions?.[i]?.width || 0`) then defaults that page's width to 0, which handle-chapter-reader.ts:321-325's spread-threshold check treats as 'narrow', silently mispairing it. There is no server log line for a single failed page fetch (the error is discarded with `_`) - only 'manga: Could not get chapter pages' (chapter_page_container.go ~139) if the WHOLE chapter fetch fails, which is a different, unrelated failure.

**If it fails, look here first:** internal/manga/chapter_page_container.go:204-249 (getPageDimensions) - check the semaphore is actually acquired/released per-goroutine (`needsFetch` gate) and that `buf, ferr := ...; if ferr != nil { return }` isn't reintroducing the old `buf, err = ...` shared-named-return pattern.


### T-MANGA-2 — Local manga provider: .cbz/.zip AND plain-directory chapters load correctly across repeated opens (fd leak)

**Covers:** MEDI-NAK-4 (page.Close() added in both branches of local.go FindChapterPages)  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Local server instance where you can write files into the directory configured as `Manga.LocalDir` (internal/core/extensions.go:29, which is set from `cfg.Offline.Dir` in config.toml/app config - check server startup config for the exact path). No Denshi/Pi needed, browser is fine.

**Preconditions:** Under that directory, create two test 'series' folders matching the Local provider's expected layout (internal/manga/providers/local.go: ID is the relative filepath, e.g. `series/chapter_1.cbz` or `series/vol1/ch1.cbz`):
 - `TestSeriesZip/chapter_1.cbz` - a zip of ~30-50 numbered jpg/png images (isFileImage filter).
 - `TestSeriesDir/chapter_1/` - a plain folder of ~30-50 numbered jpg/png images (the `default:` branch, local.go ~490-500).
Add/select 'Local' as the manga source for a test entry so the client can browse these chapters (extension shows up like any other manga provider in the entry's source picker).

**Steps:**

1. Open `TestSeriesZip` chapter_1 in the reader; confirm all pages appear, in order, image content correct.
2. Close the chapter, reopen it 15-20 times in a row (rapid navigate-away/navigate-back), alternating with...
3. ...opening `TestSeriesDir` chapter_1 the same number of times.
4. On the server host, check the process's open file descriptor count before and after the loop: on Linux/Pi `ls /proc/$(pgrep -f seanime)/fd | wc -l` (or `lsof -p <pid> | wc -l`); on a local Windows dev run, Resource Monitor's Handles column for the seanime.exe process.

**PASS looks like:** Both chapters load correctly every time (right page count, right order, right images) on every one of the 15-20 repeats. Open FD / handle count after the loop returns close to its pre-loop baseline (not growing by ~30-50 per directory-chapter open, since each open leaks exactly one os.File per page in the old code).

**FAIL looks like:** FD/handle count climbs roughly linearly with the number of directory-chapter opens (the .cbz branch was never a real fd leak per the audit - zip.OpenReader's single fd is already closed by `defer r.Close()` at local.go:420, so a regression there would NOT show up as fd growth, only as slightly higher GC pressure). Eventually (usually only reachable under sustained load, not a 20-iteration test) new chapter opens fail with an os.Open error once the process fd limit is hit.

**If it fails, look here first:** internal/manga/providers/local.go - the `default:` (plain directory) branch around line ~494-500: confirm `_ = page.Close()` runs BEFORE the `io.ReadAll` error check, not inside a `defer` in the loop (a `defer` would hold every fd open until FindChapterPages returns, which is the trap the audit fix explicitly warned against) and not only on the success path.


### T-MERGE-1 — Denshi: seek-bar OP/ED highlight now matches what auto-skip actually skips

**Covers:** MERG-MERGE-1 (mpv-core-time-range.tsx skipChapters memo now passes {guardIntro:false, heuristics:true, duration})  |  **Priority:** P1-important  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Denshi REBUILD required - this is a web/UI change (mpv-core-time-range.tsx) and Denshi bundles its own web UI, so a server redeploy does NOT update it. Run `scripts/build-denshi-local.sh --installer` (unpacked builds have an unrelated known bug per denshi memory 'mpv-prism unpacked-run' - use --installer) and test in the installed build. Browser cannot test this at all (no MpvCore there).

**Preconditions:** Two episodes with embedded chapter markers, from your library: (a) one with chapters explicitly labeled Intro/Outro (not the exact strings 'Opening'/'Ending') OR an unlabeled ~90-150s chapter near the start/end of a <=45min episode (media-core-chapters.ts SKIP_MIN/MAX_LENGTH window, duration gate `duration > SKIP_MAX_LENGTH*2 && duration < 2700`); (b) one with standard AniSkip-less native 'Opening'/'Ending'-labeled chapters as a control. Enable 'Highlight OP/ED chapters' if it's not already on by default (vc_highlightOPEDChaptersAtom / mc_highlightOPEDChapters defaults true).

**Steps:**

1. Play episode (a) in Denshi. Watch the seek bar during the Intro/Outro or unlabeled OP/ED-length chapter: it should be visually highlighted (same styling as a normal Opening/Ending chapter) BEFORE playback reaches it.
2. Let playback run into that chapter and confirm auto-skip actually fires (this already worked pre-fix, per mpv-core-player-inner.tsx:647's existing `heuristics:true`) - confirm the skipped region matches the highlighted region exactly (same start/end).
3. Play episode (b) as a control - confirm standard-labeled OP/ED chapters are still highlighted and skipped as before (no regression from the fix).

**PASS looks like:** In episode (a), the seek-bar highlight and the actually-auto-skipped region are the SAME chapter. In episode (b), behavior is unchanged from before this fix.

**FAIL looks like:** Auto-skip fires (video jumps) over a region of the seek bar that was NOT highlighted/colored beforehand - the exact 'auto-skip fires with no corresponding visual marker' symptom the finding describes. Root cause if still broken: mpv-core-time-range.tsx's `getSkipChapters(chapters, skipPatterns, {...})` call is missing `heuristics: true` and/or `duration` in its options object or dependency array.

**If it fails, look here first:** seanime-web/src/app/(main)/_features/mpv-core/mpv-core-time-range.tsx (~line 125-131) - diff against seanime-web/src/app/(main)/_features/media-core/video-core-time-range.tsx's equivalent memo (~line 142) which is the known-correct sibling; and confirm you actually rebuilt+reinstalled Denshi (stale installed build is the #1 false-negative here).


### T-EXT-PLG4-PANIC-ISOLATION — One panicking fetch response no longer kills the pump for every later fetch on the same extension

**Covers:** PLG-4  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Any running seanime server; requires a plugin/provider whose response-handling closure can be made to panic (hardest to trigger organically -- most realistic via a provider extension returning a malformed response that the JS-side handler mishandles, e.g. an extension expecting a JSON array getting an HTML error page).

**Preconditions:** This validates the fetch.go diff's own stated rationale (the per-iteration defer/recover fix: 'a bare defer inside the loop only runs when the goroutine exits, so one panicking response would kill the pump and hang every later fetch on this VM'). No automated test exists for the pump-goroutine's recover scoping specifically.

**Steps:**

1. Trigger a search against a provider/plugin known to occasionally return a response shape that causes a JS-side error inside the resolve callback (or, if reproducing organically is impractical, treat this as a code-reading spot-check rather than a live run: confirm the fix is present). 2. If reproduced: immediately run a second, normal search using the SAME extension.

**PASS looks like:** The second search completes normally -- the panic from the first response is recovered and logged, but subsequent fetches on that VM still resolve.

**FAIL looks like:** After one malformed/panicking response, every subsequent fetch() call on that same extension hangs forever (same silent-hang symptom as the other cases) because the pump goroutine's for-loop exited on the unrecovered panic and no request will ever be pumped again for that VM.

**If it fails, look here first:** Check internal/goja/goja_bindings/fetch.go's BindFetch pump (the `for fn := range f.ResponseChannel()` loop): the recover() must be inside a `func(){ defer recover(); fn() }()` called PER iteration, not a single `defer recover()` statement placed once before the loop starts.


### T-DBG-1-RESTART — Playback after a server restart (cold in-memory fileIdCache) still plays via the post-restart hydrate path

**Covers:** DBG-1  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same local-server + Denshi setup, TorBox active.

**Preconditions:** None beyond a working TorBox setup — the point of this case is that TorBox's fileIdCache (torbox.go) is an in-process map, never persisted, so it is empty on a fresh process, while the account-wide DB prewarm record (persistPrewarm, stream.go:1653) survives a restart.

**Steps:**

1. Play or preload an episode once so it gets persisted (persistPrewarm shares it account-wide in the DB). 2. Kill the local `go run .` process and restart it fresh (fully new process = empty TorBox fileIdCache). 3. In Denshi, immediately play that same episode again — this should route through the post-restart/cross-user hydrate path (`hydratePrewarmFromDB`, dispatched at stream.go:455) rather than the in-memory hit at stream.go:441. 4. Confirm normal playback.

**PASS looks like:** Plays normally; log shows the preloaded-stream line via the hydrate path; ExpectedSize is 0 here (fileIdCache empty post-restart, knownFileSizeFor fails open per stream.go:373-378, and truncatedStreamErr's `expected <= 0` short-circuit at httpstream.go:150 makes the guard inert) — so behavior must be byte-identical to pre-fix.

**FAIL looks like:** A truncation-abort or any new error appears specifically only right after a restart when the identical episode played fine before the restart; or playback silently stops using the fast prewarm path and always falls back to a slow cold resolve (watch for the "Refreshing stream link..."/"Checking stream link..." pill messages, stream.go:1621-1660, taking noticeably longer post-restart than pre-restart).

**If it fails, look here first:** Not expected from this diff by construction (0 is always inert) — if it does fail, the bug is elsewhere (e.g. s.repository.GetProvider() erroring post-restart), not in the ExpectedSize wiring itself.


### T-DBG-1-COLD-CONTROL — Fresh cold-resolve play (never preloaded) as a control/baseline — isolates whether a failure in the other cases is really this diff

**Covers:** DBG-1  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Same local-server + Denshi setup, TorBox active.

**Preconditions:** An episode that has NOT been preloaded this session (skip Continue Watching; open an untouched title from the library and manually pick a torrent, forcing AutoSelect:false so the in-memory/DB preload lookups are skipped).

**Steps:**

1. Open a title/episode never touched this session. 2. Manually select a torrent via the debrid picker and play it — this takes StreamManager.startStream's cold path (stream.go:988, `ExpectedSize: knownFileSize(provider, torrentItemId, fileId)`), which this diff does NOT touch. 3. Confirm normal playback.

**PASS looks like:** Plays normally, exactly as it did before this diff (this code path is untouched by the DBG-1 change).

**FAIL looks like:** N/A to this diff by definition — if this fails, it is a pre-existing/unrelated regression (e.g. from commit 4ebfa601 itself), not DBG-1. Use this result to disambiguate: if this ALSO fails the same way as T-DBG-1-REGRESSION, the bug is upstream in truncatedStreamErr/loadPlaybackInfo shared code, not in the ExpectedSize-wiring diff.

**If it fails, look here first:** Look at startStream's provider.GetTorrentStreamUrl call and knownFileSize(provider, torrentItemId, fileId) at stream.go:988, not at playPreloadedStream — this case exists purely to isolate blame, not to validate the fix.


### T-SCAN-1 — Library scan still logs cleanly after ScanLogger.mu removal (VET-1 regression check)

**Covers:** VET-1  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Single local Seanime instance with a configured library path (Settings > Library) containing at least a handful of video files. No special account/plugin/debrid requirement.

**Preconditions:** Library path set and scannable in Settings.

**Steps:**

1. Open the scan trigger UI (seanime-web/src/app/(main)/_features/anime-library/_containers/scanner-modal.tsx, reached from the Library page's scan button) and start a scan with default options.
2. Let it run to completion, watching the progress bar / status text update (EventScanProgress/EventScanStatus websocket pushes from internal/library/scanner/scan.go) through to 'Scan completed'.
3. Locate the scan log file the run just wrote: <server logs dir>/<YYYY-MM-DD_HH-MM-SS>-scan.log (internal/library/scanner/scan_logger.go NewScanLogger, outputDir = h.App.Config.Logs.Dir). Open it in a text editor.

**PASS looks like:** Scan completes normally (no HTTP error, 'Scan completed' status shown). The log file exists, is non-empty, and every line is a single well-formed JSON object (zerolog's default line format) — no line contains two interleaved/truncated JSON objects.

**FAIL looks like:** Log file has garbled/interleaved lines — e.g. a line containing a partial `{"level":"info",...` immediately followed mid-string by another `{"level":"warn",...` from a different writer — or the scan request itself errors (HTTP 500 from POST /api/v1/library/scan) with a message pointing into scan_logger.go.

**If it fails, look here first:** internal/library/scanner/scan_logger.go NewScanLogger/NewConsoleScanLogger (~line 23-66): confirm ThreadSafeWriteSyncer{buffer, &mu} still shares the SAME *sync.Mutex pointer across every writer (the actually-load-bearing lock, per the audit's VET-1 entry) — the field removal should only have deleted the dead, never-locked ScanLogger.mu copy, nothing else.


### T-DB-3 — Login/logout/account-switch still work without hanging after accountCache gets a mutex

**Covers:** DATA-DB-3 (accountCacheMu RWMutex guarding the package-level accountCache; lock NOT held across GetAdminUser/GetAccountByID/gormdb calls)  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Networked/password-protected server setup is best (so login/logout is meaningful and multiple accounts exist), but a local password-less admin install can still exercise UpsertAccount/GetAccount via connect/disconnect AniList.

**Preconditions:** None special. This test cannot prove the underlying race is fixed (races are probabilistic and there's no `go test -race` run in the normal build/deploy pipeline - scripts/check.sh does a plain build, not -race) - it only guards against the specific regression the audit called out: a naive fix that holds the lock across the DB calls inside GetAccount would deadlock or serialize every request.

**Steps:**

1. Log out of AniList in Settings, then log back in - confirm it completes without hanging (should be near-instant, matching current behavior).
2. With the server running, open the app in two browser tabs/windows and hit any two pages that hit GetAnilistToken concurrently (e.g. refresh the library page in both tabs at the same moment, or trigger a manga/anime search in one tab while refreshing settings in the other - both paths call GetAccount/GetAnilistToken per internal/database/db/account.go).
3. If multiple user accounts exist, switch between accounts / promote a different user to admin and confirm AniList data still resolves for the correct account afterward.

**PASS looks like:** No request ever hangs waiting on account data; login/logout/account-switch complete in normal time; the app is usable throughout with no stall.

**FAIL looks like:** Any request that touches AniList account data (library load, search, plugin calls, Settings save) hangs indefinitely or takes drastically longer than before, especially under concurrent load. This would indicate the write lock is held across a slow DB call.

**If it fails, look here first:** internal/database/db/account.go - `GetAccount()` (~line 40-63): confirm `cachedAccount()`/`setCachedAccount()` (RLock/Lock helpers) are only called around the direct pointer read/write, and that `db.GetAdminUser()`, `db.GetAccountByID()`, and `db.gormdb.Last(&acc)` execute with NO accountCacheMu lock held.


### T-DB-4 — Mediastream/transcode still functions after filecache TrimMediastreamVideoFiles only wipes the store map when it actually trimmed

**Covers:** DATA-DB-4 (c.stores wipe moved inside the `if len(files) > 10` block; `return err` -> `return nil`)  |  **Priority:** P2-nice  |  **Only verification that exists:** **YES — nothing else checks this**

**Environment:** Local or Pi server with Mediastream transcoding enabled (Settings -> Playback/Mediastream, TranscodeEnabled). No Denshi/Pi/multi-account specifically required, but real transcode playback needs a video that requires transcoding (non-directplay codec/container).

**Preconditions:** This is a low-stakes correctness/regression check, not a bug-catching test - the audit's own analysis found the pre-fix behavior was a near no-op (the wipe forced an on-disk re-read of unchanged data, no data loss, since every filecache write is write-through via store.saveToFile()). Its value here is confirming the refactor (moving one line, changing the return value) didn't break trimming or transcoding.

**Steps:**

1. Play a video that requires transcoding; confirm it starts and plays smoothly (this exercises MediastreamRepository + the videofiles cache dir).
2. In Settings, save/re-save the Mediastream/transcode settings a few times in a row (each save calls InitOrRefreshMediastreamSettings -> handlers/mediastream.go:63 -> internal/core/modules.go:867-901, which calls TrimMediastreamVideoFiles when TranscodeEnabled is true).
3. After each save, immediately re-check: the video still plays / resumes correctly, and unrelated cached data elsewhere in the app (library list, continue-watching row, manga entries) doesn't blank out, error, or visibly re-fetch/flicker right after the settings save.

**PASS looks like:** Transcoded playback keeps working across repeated settings saves. No error toasts, no stale/blank data anywhere in the app immediately following a mediastream settings save.

**FAIL looks like:** Transcoding fails to start/errors after a settings save, OR unrelated cached UI data (library, continuity/last-watched) visibly refetches/flickers/blanks right after saving mediastream settings when there were <=10 leftover files in the videofiles cache dir (that's the specific condition where pre-fix code wiped everything and post-fix code doesn't touch the map at all).

**If it fails, look here first:** internal/util/filecache/filecache.go TrimMediastreamVideoFiles (~line 465-484) - confirm `c.stores = make(map[string]*CacheStore)` sits INSIDE the `if len(files) > 10 { ... }` block (not after it) and the function returns `nil`, not the (always-nil) `err` from the earlier ReadDir.

