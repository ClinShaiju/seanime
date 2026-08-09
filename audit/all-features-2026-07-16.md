# Seanime full-feature audit — 2026-07-16

Scope: repo-wide (all subsystems), hunting bugs, races, leaks, regressions, perf, stability.
Baseline at audit start: `scripts/check.sh` → **GO BUILD OK / WEB TYPECHECK OK** (clean).
Method: 12 sonnet finders fanned across subsystem slices → Opus adversarial verify (verifier's job is to
*refute*) → only survivors listed. Plus an independent `go vet ./internal/...` pass.

Status legend: **open** / **fixed** / **deferred**. Verdicts: CONFIRMED (independently proven) /
PLAUSIBLE (real-looking, reachability unproven) / REFUTED (dropped, not listed).

---

## A. `go vet` pass (independent of the finders)

`go vet ./internal/...` produced 8 diagnostics. All were run to ground by reading the code. **None are live
bugs** — this is a good-news section, recorded so the next audit doesn't re-chase them.

### VET-1 — `ScanLogger.mu` is a dead, copied mutex — **open** (low)

- **Location:** `internal/library/scanner/scan_logger.go:19` (field), `:45-50` (construction)
- **What:** `NewScanLogger` builds `mu := sync.Mutex{}`, hands `&mu` to `ThreadSafeWriteSyncer{buffer, &mu}`,
  then stores `mu` **by value** into `&ScanLogger{..., mu}`. vet flags the lock copy.
- **Not a race — verified.** The `ScanLogger.mu` field is *never locked anywhere in the repo*. The only lock
  is `ThreadSafeWriteSyncer.mu *sync.Mutex` (`:115-120`), which holds the pointer to the heap-escaped local.
  All writers share that one pointer, so log-write serialization is genuinely correct today.
- **Why it still matters:** the field sits on the struct looking like it guards `buffer`. A future caller that
  locks `ScanLogger.mu` would get zero protection and a silent race. It is a trap, not a defect.
- **Fix:** delete the `mu` field from the struct (and the `mu: sync.Mutex{}` at `:63`). Deletion, not a lock.
- **Regression risk:** none — field has no readers. Pure removal.

### VET-2 — `chunkSize = chunkSize` self-assign in dummy debrid — **open** (low)

- **Location:** `internal/debrid/dummy/dummy.go:532-535`
- **What:** `chunkSize := settings.ChunkSize` **shadows** the package const `chunkSize = 64 * 1024` (`:35`),
  so the intended default `if chunkSize <= 0 { chunkSize = <const> }` self-assigns and does nothing.
- **Dead branch — verified.** The only source of `settings` is `d.settings()` (`:324-335`), which always
  returns `normalizeSettings(...)`, and that forces `ret.ChunkSize = chunkSize` when `<= 0` (`:601-603`).
  So `ChunkSize` is never `<= 0` on entry. Had it been reachable, `buf := make([]byte, 0)` → `io.ReadFull`
  returns `0, nil` → `read == 0` → `break`: a silent 0-byte success (truncated stream), not a hang.
- **Why it matters:** dev/test-only provider; latent trap if a future caller bypasses `normalizeSettings`.
- **Fix:** delete the dead `if` (preferred), or rename the local to un-shadow the const.
- **Regression risk:** none — branch is unreachable.

### VET-3 — six transcoder debug timings always log `0.00s` — **open** (low)

- **Location:** `internal/mediastream/transcoder/transcoder.go:107,127,153,179,205,234`
- **What:** `defer t.logger.Trace().Msgf("...in %.2fs", time.Since(start).Seconds())` — deferred call
  **arguments are evaluated at the `defer` statement**, not at return. `time.Since(start)` is therefore ~0
  every time.
- **Why it matters:** telemetry only. Every one of these six traces reports `0.00s`, so the timings are
  worthless for diagnosing transcoder slowness — actively misleading if someone trusts them. Gated behind
  `debugStream` at Trace level, so no user-facing impact.
- **Fix:** wrap in a closure: `defer func() { t.logger.Trace().Msgf("...", time.Since(start).Seconds()) }()`.
- **Regression risk:** none — log-only, inside an existing `if debugStream` block.

---

## B. Subsystem findings

<!-- workflow results appended below as they are confirmed -->

### B1. Confirmed (independently proven by an adversarial Opus verifier)

#### HAND-AUTH-1 — Privileged torrent-client executable gate is bypassed by any password holder — route has zero auth middleware — **open** (high, finder said critical)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** handlers-auth
- **Location:** `internal/handlers/torrent_client.go:HandleTorrentClientDownload (calls guardPrivilegedTorrentClient, local_security.go:413); routes.go:314`
- **What:** POST /api/v1/torrent-client/download has no h.UserOnly/h.AdminOnly middleware at the router (routes.go:314), and guardStrictLocalOnlyAction/guardStrictFilesystemPath are no-ops unless the strict security level is on. The only guard against using a privileged (admin-configured custom-path) torrent client is guardPrivilegedTorrentClient, which allows the action whenever isTrustedRequest(req, serverPassword) is true — and isTrustedRequest returns true unconditionally whenever serverPassword != "" (local_security.go:278-281). So on a networked/password-protected multi-user server, the intended admin-only gate for driving a privileged external torrent client evaluates to 'always allowed' for anyone who passed the outer shared-password check, including a non-admin user or even a session-less anon request.
- **Why it matters:** Any user who merely knows the shared server password (not necessarily a registered/admin account) can add magnets/torrents through the admin's configured qBittorrent/Transmission client and choose the download destination — full torrent-download capability plus a hook into whatever privileged client binary the admin set up, undermining the entire admin-owns-shared-infra-plane model this fork's multi-user auth was built around.
- **Evidence:** local_security.go: `func isTrustedRequest(req *http.Request, serverPassword string) bool {\n\tif serverPassword != "" || security.IsLax() {\n\t\treturn true\n\t}\n...}` and `func (h *Handler) guardPrivilegedTorrentClient(...) error {\n...\n\tif isTrustedRequest(c.Request(), h.App.Config.Server.Password) || !isPrivilegedTorrentClient(settings) {\n\t\treturn nil\n\t}\n\treturn respondWithAbort(...)\n}`. routes.go:314: `v1.POST("/torrent-client/download", h.HandleTorrentClientDownload)` — no middleware arg, unlike sibling privileged routes (`/settings`, `/mediastream/settings` etc.) which all carry `h.AdminOnly`.
- **Verifier:** Quoted code is verbatim-accurate and current. isTrustedRequest (local_security.go:278-287) does return true unconditionally when serverPassword != "". guardPrivilegedTorrentClient (413-427) calls it and returns nil, so the privileged gate evaluates to "always allowed" for any password holder. routes.go:314 genuinely has no middleware. I proved the full chain is reachable in the fork's prod config: security default mode is "" (security.go:9-14, NormalizeMode default), so IsStrict()==false makes guardStrictLocalOnlyAction (strict_security.go:17-23) and the guard's first branch (line 419) genuine no-ops. Request path: POST /api/v1/torrent-client/download with X-Seanime-Token = ServerPasswordHash and no Bearer → OptionalAuthMiddleware passes (server_auth_middleware.go:37-40) → IdentityMiddleware sets NO identity (identity.go:52-53, case 3) → no UserOnly → guardPrivilegedTorrentClient returns nil → reaches AddMagnets(magnets, b.Destination). The library-path restriction is commented out (torrent_client.go:309-316) and only filepath.IsAbs is enforced, so destination is any absolute path. The privileged exec is real: Start() → repository.go:124 CheckStart() → qbittorrent/start.go:50-51 util.NewCmd(exe); cmd.Start(). Decisively, this is an oversight not intentional design: identity.go:75-80 documents UserOnly as being for "debrid/torrent *operation* endpoints (add torrent, file previews, etc.) so an anon may browse but never drive debrid/torrent work", and the exact analogue /debrid/torrents carries h.UserOnly (routes.go:584) while /torrent-client/download carries nothing; identity.go:26-34 states the intended ceiling ("Knowing the shared server password is not enough... the outer network gate is separated from identity"); and guardPrivilegedExtensionManagement (local_security.go:308-315) carries a comment describing this precise isTrustedRequest weakness and applies the IsAdmin fix — proving the author knew the pattern and missed this route. I attacked the claim on middleware (OptionalAuthMiddleware does enforce, but only the shared password — it does not establish identity), on strict-mode guards, on caller-side validation, and on intentionality; none refute it.
- **Fix:** Add h.UserOnly (NOT AdminOnly) at the router to the torrent-client operation routes, matching the existing /debrid/torrents* precedent at routes.go:584-590 and the fork's own UserOnly contract in identity.go:75-80: routes.go:314 /torrent-client/download, :317 /torrent-client/action, :318 /torrent-client/get-files, :319 /torrent-client/rule-magnet. This closes the anon (password-only, no session) hole, which is the actually-exploitable part, without touching the shared guard. Do NOT add an IsAdmin check inside guardPrivilegedTorrentClient as the finder proposes — it is shared with HandleGetActiveTorrentList (the polled /torrent-client/list) and would 403 the torrent list for all non-admins whenever a custom executable path is configured. If the privileged-executable *launch* specifically must be admin-gated, gate it narrowly at the launch site (the h.App.TorrentClientRepository.Start() call in HandleTorrentClientDownload/HandleTorrentClientAddMagnetFromRule) rather than at the guard shared with the read-only list path — i.e. let a non-admin drive an already-running client but not cause the server to spawn the admin's binary. Separately worth noting (out of scope for this finding but same handler): the destination is only validated by filepath.IsAbs because the library-path check at torrent_client.go:309-316 is commented out, so any authorized user can write torrent content to an arbitrary absolute path.
- **Regression risk:** The finder's proposed fix (IsAdmin inside guardPrivilegedTorrentClient) would break currently-working behavior at two of the guard's three callers, which find_referencing_symbols shows are HandleGetActiveTorrentList (torrent_client.go:45), HandleTorrentClientDownload (:321), and HandleTorrentClientAddMagnetFromRule (:481). (1) HandleGetActiveTorrentList backs GET /torrent-client/list — a high-frequency poll explicitly in routes.go's log-skip list (urisToSkip:49). Adding IsAdmin to the shared guard would 403 the entire torrent-list UI for every non-admin user on any install where the admin set a custom qBittorrent/Transmission path (isPrivilegedTorrentClient only trips on hasCustomExecutablePath), turning a read-only list into an admin-only view and spamming failed polls. (2) It would also block regular users from downloading at all on custom-path setups, contradicting the fork's own model where torrent operations are UserOnly (user-level), not AdminOnly. (3) The separate half of the fix — adding h.UserOnly to /torrent-client/download and /torrent-client/action — is the correct direction but breaks any client that authenticates with the server password only and never establishes a user session (older Tenji/Denshi builds or scripted clients sending X-Seanime-Token without a Bearer token): on a password-protected server they would lose torrent downloading entirely with a 403, since IdentityMiddleware gives them no identity and dataUserID(c)==0. HandleTorrentClientAddMagnetFromRule is auto-downloader-rule-driven, so gating it on a user session needs checking that its invocation always carries one.

#### HAND-AUTH-2 — /torrent-client/action has no auth gate for any action except 'open' — non-admin can remove/rename/pause-all/move-storage torrents or add-magnet to an arbitrary directory — **open** (high)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** handlers-auth
- **Location:** `internal/handlers/torrent_client.go:HandleTorrentClientAction (lines 68-180); routes.go:317`
- **What:** routes.go:317 registers POST /api/v1/torrent-client/action with no middleware. Inside the handler, only the "open" action calls guardPrivilegedLocalExecution; every other action (pause, resume, remove, pause-all, resume-all, force-start, move-storage, recheck, reannounce, add-tracker, remove-tracker, rename, set-limits, add-magnet) runs with zero authorization or user-identity check. add-magnet in particular passes an attacker-controlled `b.Dir` straight to `client.AddMagnet(b.Magnet, b.Dir)` with none of the guardStrictFilesystemPath/absolute-path validation that HandleTorrentClientDownload applies to its Destination field.
- **Why it matters:** Any request that passes the outer shared-password gate (which, per AUTH-1's isTrustedRequest semantics, is effectively anyone with the password on a networked server) can wipe/rename all torrents, throttle bandwidth for the whole server, or download a magnet to an arbitrary filesystem path — a destructive, unauthenticated-by-role admin capability.
- **Evidence:** routes.go:317: `v1.POST("/torrent-client/action", h.HandleTorrentClientAction)`. torrent_client.go: the switch over b.Action only guards the "open" branch (`if err := h.guardPrivilegedLocalExecution(c); err != nil { return err }`); the `case "pause-all", ... "add-magnet":` block runs `client.AddMagnet(b.Magnet, b.Dir)` etc. with no preceding guard call at all.
- **Verifier:** Tried hard to refute via the global middleware and failed. Code read: routes.go:316 registers `v1.POST("/torrent-client/action", h.HandleTorrentClientAction)` with no middleware (find_referencing_symbols confirms InitRoutes is the sole registration). torrent_client.go:69-181 matches the finder: only `case "open"` calls guardPrivilegedLocalExecution (line 128); `pause`/`resume`/`remove` (99-113) are unguarded for any provider, and the block at 132-173 reaches `client.MoveStorage(b.Hash, b.Dir)` and `client.AddMagnet(b.Magnet, b.Dir)` unguarded. The handler never calls guardPrivilegedTorrentClient at all, unlike siblings HandleGetActiveTorrentList (line 46) and HandleTorrentClientDownload.

Best refutation candidate: `e.Use(h.trustedLocalRequestMiddleware)` (routes.go:25) DOES cover this route — isPathNeedingTrustedLocalBoundary returns true for any "/api/" prefix. But it delegates to isRequestPermitted (local_security.go:181), whose first line is `if serverPassword != "" || security.IsLax() { return true }`. isTrustedRequest is identical. So on a networked password-protected server that middleware is a NO-OP. The only remaining gate is v1.Use(h.OptionalAuthMiddleware), which merely proves knowledge of the shared password (passwordHash == h.App.ServerPasswordHash → next). No role/identity check reaches the handler.

Reachability by a non-admin is proven by IdentityMiddleware (identity.go:34-54): with a password set and no bearer session, NO identity is assigned ("Networked server, no session → no identity"); password-less local installs get the admin injected. So a password-only anon (dataUserID==0, CurrentUserRole=="") or any logged-in regular user reaches every unguarded branch. Note move-storage/add-magnet additionally require provider==SeanimeClient (line 134), but pause/resume/remove work for any provider.

Intent is settled by the fork's own adjacent code, which refutes "intentional fork behavior": routes.go:582-590 gates the debrid endpoints with h.UserOnly under the comment "anon (server-password only, no user session) may browse but not drive debrid work", and guardPrivilegedLocalExecution states "Knowing the shared password is not the same as being the admin." This route is a missed application of the fork's own authorization model.

Correction to the finder: guardStrictFilesystemPath (strict_security.go:26) is a no-op unless security.IsStrict(), so the finder overstates it as a default-mode protection; HandleTorrentClientDownload's real default-mode guards on Destination are guardStrictLocalOnlyAction + filepath.IsAbs. Also minor: route is line 316 not 317, handler 69-181 not 68-180. Substance unaffected.
- **Fix:** Prefer h.UserOnly on the route (routes.go:316) over h.AdminOnly — AdminOnly would break the legitimate case where a logged-in regular user manages torrents they added via the equally-ungated /torrent-client/download. UserOnly matches the precedent set by the debrid routes at routes.go:584-590 and is safe for password-less local installs, since IdentityMiddleware injects the admin identity there.

Additionally, gate the genuinely admin-scoped, server-wide branches inside the handler with an explicit h.IsAdmin(c) check rather than relying on the route middleware: "pause-all", "resume-all", "set-limits" (global bandwidth throttle = whole-server DoS), and "move-storage"/"add-magnet" (arbitrary filesystem write as the server process user).

For b.Dir on move-storage/add-magnet, do NOT rely on guardStrictFilesystemPath alone — it returns nil unless security.IsStrict(). Mirror HandleTorrentClientDownload's real default-mode validation: reject empty, require filepath.IsAbs(b.Dir), and call guardStrictLocalOnlyAction + guardStrictFilesystemPath. Also add the missing h.guardPrivilegedTorrentClient(c, h.App.Settings) call so the strict-local boundary applies to actions against an external client, as it already does for list/download.
- **Regression risk:** Traced via find_referencing_symbols + IdentityMiddleware/CurrentUserRole:

1. h.AdminOnly (the finder's first-listed option) is the sharp risk: CurrentUserRole reads ctxUserRole, set only by setIdentity. On a networked server a logged-in REGULAR user has a non-admin role, so AdminOnly 403s them. Since GET /torrent-client/list and POST /torrent-client/download are themselves ungated, a regular user could still add torrents and see them listed but every pause/resume/remove button would 403 — an asymmetric, visibly broken torrent UI. UserOnly avoids this.

2. h.UserOnly breaks password-only anon sessions on a networked server: anon still sees the torrent list (GET /torrent-client/list is ungated) but all action buttons 403. This is the intended tradeoff per the debrid precedent, but it IS a live behavior change for shared-password-no-login setups — specifically the documented Denshi serverUrl → Pi configuration, which must carry a bearer session token or it will lose all torrent controls.

3. Local password-less installs are NOT at risk: IdentityMiddleware injects the admin user when Server.Password == "", so both UserOnly and AdminOnly pass. Denshi/desktop single-user is unaffected.

4. Adding strict-roots validation to b.Dir for move-storage can reject currently-working moves: move-storage legitimately targets user-chosen directories outside strictFilesystemRoots, so in strict mode an isStrictFsPathAllowed check would break existing relocations. Scope the path check to add-magnet, or widen the allowed roots to include configured torrent save paths.

5. Adding guardPrivilegedTorrentClient could newly 403 external-client (qBittorrent/Transmission) actions in strict mode from non-trusted-local origins — currently permitted because the handler omits the guard entirely.

#### HAND-AUTH-3 — Auto-downloader rule/profile CRUD endpoints have no auth gate — any user can create/edit/delete server-wide download rules — **open** (high)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** handlers-auth
- **Location:** `internal/handlers/auto_downloader.go: HandleCreateAutoDownloaderRule/HandleUpdateAutoDownloaderRule/HandleDeleteAutoDownloaderRule/HandleCreate|Update|DeleteAutoDownloaderProfile; routes.go:189-200`
- **What:** routes.go:189-200 register POST/PATCH/DELETE for /auto-downloader/rule and /auto-downloader/profile with no middleware (no h.UserOnly, no h.AdminOnly), unlike the sibling /settings/auto-downloader route which is h.AdminOnly-gated. The handler bodies contain no RequireAdmin/dataUserID check either — the only validation is guardStrictFilesystemPath (a no-op outside strict security mode) on the destination path.
- **Why it matters:** These rules are server-wide (shared filecacher/db rows, not scoped by user id) and drive automatic torrent downloads into arbitrary library destinations. Any authenticated non-admin user — or, since there's no UserOnly gate either, any request that merely knows the shared server password — can create rules that auto-download content to any path the strict-mode check doesn't block, or delete all of the admin's existing rules (including the "-1" bulk-delete branch).
- **Evidence:** routes.go: `v1.POST("/auto-downloader/rule", h.HandleCreateAutoDownloaderRule)` / `v1.PATCH("/auto-downloader/rule", h.HandleUpdateAutoDownloaderRule)` / `v1.DELETE("/auto-downloader/rule/:id", h.HandleDeleteAutoDownloaderRule)` / same pattern for /auto-downloader/profile — none carry h.AdminOnly or h.UserOnly, and none of HandleCreateAutoDownloaderRule/HandleUpdateAutoDownloaderRule/HandleDeleteAutoDownloaderRule/HandleCreateAutoDownloaderProfile/HandleUpdateAutoDownloaderProfile/HandleDeleteAutoDownloaderProfile call RequireAdmin or dataUserID.
- **Verifier:** Every element of the claim verified against current code. routes.go:190-201 registers POST/PATCH/DELETE for /auto-downloader/rule and /auto-downloader/profile with no per-route middleware, in direct contrast to routes.go:181 `v1.PATCH("/settings/auto-downloader", h.HandleSaveAutoDownloaderSettings, h.AdminOnly)`. Group middleware cannot save it: v1 gets only OptionalAuthMiddleware (server_auth_middleware.go — validates X-Seanime-Token against ServerPasswordHash, a network gate, not identity), IdentityMiddleware (identity.go:35-54 — resolves a user or leaves none, always `return next(c)`, never rejects), and FeaturesMiddleware (a config kill-switch on core.ManageAutoDownloader, not per-user). Handler bodies auto_downloader.go:113-232 contain no RequireAdmin/dataUserID/guardStreamingUser — confirmed by grep over the whole file; the only checks are non-empty destination, MediaId!=0, filepath.IsAbs, and guardStrictFilesystemPath, which strict_security.go:27-30 short-circuits to nil unless security.IsStrict() (SecureModeDefault = "" per security.go:9-14, so off by default). Data is genuinely server-wide: models.go:343-351 defines AutoDownloaderRule/AutoDownloaderProfile as BaseModel + Value []byte with no user-id column, and db_bridge.InsertAutoDownloaderRule(h.App.Database, rule) takes no user scope. Non-admin actors are real: models.go:37-38 defines UserRoleUser alongside UserRoleAdmin, and /user/register is a live route. Reachability proven end-to-end: a request bearing only the correct X-Seanime-Token (or a Bearer session for a role="user" account) passes OptionalAuthMiddleware, acquires no/non-admin identity in IdentityMiddleware, is not filtered by FeaturesMiddleware unless the feature is globally disabled, and lands in HandleCreateAutoDownloaderRule which inserts a rule with any absolute Destination. The delete path (auto_downloader.go:203+) includes the id==-1 bulk branch that iterates all rules and deletes every finished-airing one, exactly as claimed. Decisively, this violates the fork's own written invariant in IdentityMiddleware's doc comment (identity.go:30-33): "Knowing the shared server password is not enough to configure the server; admin actions require logging in as the admin." The finder's only inaccuracies are scope-narrow, not wrong: the sibling /auto-downloader/run, /auto-downloader/run/simulation, and DELETE /auto-downloader/item are equally ungated and belong in the same fix.
- **Fix:** Gate the mutations admin-only, but cover the full ungated surface rather than just rule/profile, and gate the UI in the same change. Server (routes.go): add h.AdminOnly to POST/PATCH /auto-downloader/rule, DELETE /auto-downloader/rule/:id, POST/PATCH /auto-downloader/profile, DELETE /auto-downloader/profile/:id, and also to POST /auto-downloader/run, POST /auto-downloader/run/simulation, and DELETE /auto-downloader/item — run triggers real downloads from existing rules and item-delete mutates the shared queue, so gating only rule/profile leaves the escalation half-open. Leave the GET routes (rule/:id, rule/anime/:id, rules, profiles, profile/:id, items) ungated so non-admins can still see what the auto-downloader is doing, matching the existing GET /settings vs PATCH /settings split. Frontend (same commit): hide the auto-downloader sidebar entry, page, and the per-anime add-rule button for role != "admin", using whatever the user-me role field exposes, so non-admins never see controls that will 403. Do not use h.UserOnly here — it only blocks networked-anon and would still let any registered role="user" account rewrite server-wide rules, which is the actual finding. Note for the fix PR: verify the fresh-install path (no admin row yet) still reaches getting-started, since AdminOnly depends on GetAdminUser() resolving.
- **Regression risk:** Three concrete breaks, one of them likely. (1) Frontend has NO admin gating to match: grep for isAdmin/role across seanime-web/src/app/(main)/auto-downloader/page.tsx and _features/anime-library/_containers/anime-auto-downloader-button.tsx returns nothing (only an unrelated SVG role="img" in main-sidebar.tsx). Adding AdminOnly server-side leaves every non-admin user with a fully rendered auto-downloader page, rule form, profiles tab, and per-anime "add rule" button whose every save/delete 403s — a silent-failure UI regression. The fix must ship with matching frontend role gating (hide the nav entry, page, and entry-page button for role="user"). (2) Networked-anon clients break: any Denshi/Tenji install pointed at the Pi that sends only X-Seanime-Token (server password) and never performs a /user/login currently manages rules fine; post-fix dataUserID(c)==0 → IsAdmin false → 403. This is the same regression class recorded in the client-identity-restart-bug and admin-streaming-double-claim memories, where auth hardening silently killed working clients. (3) Local password-less installs are mostly safe — IdentityMiddleware falls back to GetAdminUser() so AdminOnly passes — but if GetAdminUser() errors or returns nil (fresh install before the getting-started flow creates the admin row), AdminOnly 403s even locally. That risk is already accepted for the AdminOnly-gated /start and /settings routes, so it is not new, but a fresh-install smoke test is warranted. Blast radius is confined to the handlers package and the auto-downloader web page — db_bridge.InsertAutoDownloaderRule/UpdateAutoDownloaderRule/DeleteAutoDownloaderRule signatures are untouched, so the internal auto-downloader engine that consumes these rows is unaffected.

#### HAND-AUTH-4 — Extension user-config GET/POST endpoints have no auth gate — server-wide extension config (can hold provider API keys) readable and overwritable by anyone — **open** (high)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** handlers-auth
- **Location:** `internal/handlers/extensions.go: HandleGetExtensionUserConfig (504-511), HandleSaveExtensionUserConfig (518-541); routes.go:541-542`
- **What:** GET /extensions/user-config/:id and POST /extensions/user-config are registered with no middleware and neither handler calls guardPrivilegedExtensionManagement or any admin/user check (unlike every other extension-management handler in the same file, e.g. HandleInstallExternalExtension, HandleUninstallExternalExtension, all of which call h.guardPrivilegedExtensionManagement(c)). SaveExtensionUserConfig persists to a global filecache bucket keyed only by extension id (extension_repo/userconfig.go:127-148, `filecache.NewPermanentBucket(getExtensionUserConfigBucketKey(id))`), i.e. it is shared server-wide config, not per logged-in user despite the name, and it also triggers an extension reload.
- **Why it matters:** Any request that passes the outer shared-password gate can read arbitrary extension configuration values (which are free-form `map[string]string` and can include provider API keys/tokens configured by the admin) and can silently overwrite them for the whole server, forcing an extension reload — both an information-disclosure and a tamper/DoS vector reachable by non-admin users.
- **Evidence:** routes.go: `v1Extensions.GET("/user-config/:id", h.HandleGetExtensionUserConfig)` / `v1Extensions.POST("/user-config", h.HandleSaveExtensionUserConfig)` — no middleware, contrast with `v1Extensions.POST("/external/install", h.HandleInstallExternalExtension)` where the handler itself calls `h.guardPrivilegedExtensionManagement(c)`. HandleSaveExtensionUserConfig body has no such call before `h.App.ExtensionRepository.SaveExtensionUserConfig(b.ID, config)`.
- **Verifier:** Every quoted element is real and current — nothing hallucinated or paraphrased wrong.

1. HandleGetExtensionUserConfig (extensions.go:503-510) and HandleSaveExtensionUserConfig (517-540) — read in full. Neither contains any guard/admin/role call. GET goes straight to `h.App.ExtensionRepository.GetExtensionUserConfig(id)`; POST binds the body and calls `SaveExtensionUserConfig(b.ID, config)` with zero authorization and no validation that b.ID is even a known extension.

2. routes.go:541-542 confirmed verbatim: `v1Extensions.GET("/user-config/:id", ...)` / `v1Extensions.POST("/user-config", ...)` — no per-route middleware.

3. The contrast is real, not invented: HandleInstallExternalExtension (extensions.go:50-70) does call `h.guardPrivilegedExtensionManagement(c)` before acting.

4. The only group middleware is `v1.Use(h.OptionalAuthMiddleware)` (routes.go:130). I read it (server_auth_middleware.go:10-84): it passes any request whose `X-Seanime-Token` equals `h.App.ServerPasswordHash` — the *shared* password. It performs no role check. So it does not save the handlers.

5. Password-holder != admin is this fork's own established threat model, not my invention. A real role system exists (`IsAdmin` → `CurrentUserRole(c) == models.UserRoleAdmin`, identity.go:201), and guardPrivilegedExtensionManagement's own comment (local_security.go:302-325) says: "canUsePrivilegedExtensionManagement ultimately trusts any password holder (isTrustedRequest), so without this check any regular user or browse-only anon could install arbitrary extension code." The user-config handlers are exactly the hole that comment describes.

6. Storage is genuinely server-wide despite the "user" name: `getExtensionUserConfigBucketKey(extId) = fmt.Sprintf("ext_user_config_%s", extId)` (userconfig.go:11-13) — keyed on extension id only, no user component. SaveExtensionUserConfig (127-148) writes that global bucket then reloads the extension.

The finder actually UNDER-states the impact. loadUserConfig (userconfig.go:59-67) does `strings.ReplaceAll(ext.Payload, "{{field}}", savedValue)` — saved values are textually substituted into the extension's JS source. So a non-admin password holder can POST a crafted value and trigger the reload, injecting arbitrary JS into the shared plugin sandbox. That is the precise privilege escalation guardPrivilegedExtensionManagement exists to block, reached via an unguarded side door. Disclosure/tamper is unconditional; code injection is conditional on an installed extension declaring UserConfig fields (ConfigFieldType text/switch/select, extension.go:194-220).

I could not find any caller-side, middleware, or config-side check that rescues this path.
- **Fix:** The finder's fix is directionally right but incomplete on two points.

1. POST — add `if err := h.guardPrivilegedExtensionManagement(c); err != nil { return err }` after `c.Bind(&b)`, matching HandleInstallExternalExtension's placement exactly. This is the important one (it closes the payload-injection escalation).

2. GET — prefer `h.RequireAdmin(c)` over the full privileged guard. Reason: guardPrivilegedExtensionManagement bundles a `security.IsStrict() && !isRequestFromTrustedLocal(c.Request())` clause that would newly 403 a *remote admin* merely VIEWING extension config. For a read-only path RequireAdmin gets the confidentiality win without the strict-mode remote-local trap. (If you accept that strict mode should block remote reads too, the finder's uniform guard is defensible — but it is a deliberate behavior change, not a no-op.)

3. Not mentioned by the finder, worth folding in: SaveExtensionUserConfig accepts any `b.ID` with no existence check, writing an arbitrary attacker-named `ext_user_config_<id>` bucket to disk — an unbounded filecache-write primitive. Validate b.ID against the extension bank / isValidExtensionIDString before the write.

4. Longer term the "user config" name is a lie — it is server-wide state. If per-user extension config is actually the intent, the bucket key needs a user component; otherwise rename to make the shared-admin-owned nature obvious so this gap is not reintroduced.
- **Regression risk:** Concrete, and mostly on the GET side:

1. Strict-mode remote admin breakage (the real one). If GET gets the full guardPrivilegedExtensionManagement, its `security.IsStrict() && !isRequestFromTrustedLocal` clause makes a remote admin — exactly this fork's deployment shape, Denshi/browser hitting the Pi at seanime.clinshaiju.dev — get 403 when simply opening an extension's config form, which works today. `seanime-web/src/app/(main)/extensions/_containers/extension-user-config.tsx:49` calls useGetExtensionUserConfig on render, so the form would render as an error/empty state rather than fail loudly at save time. Using RequireAdmin on GET avoids this.

2. Non-admin UI degradation. If the /extensions page is reachable by non-admin users at all, that config panel starts 403-ing for them. That is the intended security outcome, but it surfaces as a broken-looking panel unless the UI also hides it for non-admins — worth a paired frontend gate so the failure is not silent.

3. Single-user/local installs are NOT at risk: with `Password == ""` the admin branch is skipped and, absent strict mode, `canUsePrivilegedExtensionManagement` → `isTrustedRequest(req, "")` returns true, so the guard is a pass-through. No regression for the default local deployment.

4. POST-side risk is essentially nil — it aligns with sibling handlers that already carry the guard, and any client that legitimately saves extension config is admin-driven.

#### LIBR-SYNC-1 — Unsynchronized map read/write on Syncer.queueState — crashes the process — **open** (high, finder said critical)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** library-local
- **Location:** `internal/local/sync.go:170-176 (GetQueueState) vs :96-112 / :121-137 (writers); exposed via internal/handlers/local.go:160`
- **What:** GetQueueState() returns `q.queueState` (a struct holding `map[int]*QueueMediaTask` fields) without ever acquiring `q.queueStateMu`. Meanwhile `processAnimeJobs`/`processMangaJobs` (two long-running background goroutines started in NewQueue) mutate `q.queueState.AnimeTasks`/`MangaTasks` — assignment and `delete()` — while holding `q.queueStateMu.Lock()`. GetQueueState is called directly from the HTTP handler `HandleLocalGetSyncQueueState` (`GET /api/v1/local/queue`), i.e. from arbitrary request-goroutines racing the sync goroutines with no synchronization at all.
- **Why it matters:** Concurrent unsynchronized read + write on the same Go map is not just 'stale data' — the Go runtime detects it and calls `fatal error: concurrent map read and map write`, which is unrecoverable (defer/recover does not stop it) and kills the whole server process. Since the frontend is expected to poll this exact endpoint while a sync job is running (that's its purpose — showing queue progress), this is a realistic, not contrived, crash scenario.
- **Evidence:** func (q *Syncer) GetQueueState() QueueState {
	return q.queueState
}
... called unguarded from internal/handlers/local.go:160: `return h.RespondWithData(c, h.App.LocalManager.GetSyncer().GetQueueState())`
vs. writer: `q.queueStateMu.Lock(); q.queueState.AnimeTasks[job.Diff.AnimeEntry.Media.ID] = &QueueMediaTask{...}; q.SendQueueStateToClient(); q.queueStateMu.Unlock()` (sync.go:96-104)
- **Verifier:** Verified every claim against the source; the finder's quotes are accurate (minor line-span imprecision only: GetQueueState is sync.go:170-172, the finder's "170-176" over-spans into SendQueueStateToClient).

REACHABILITY PROVEN, three links:
1. Writers exist and are unconditionally live. manager.go:175 `ret.syncer = NewQueue(ret)` (no feature gate), and NewQueue spawns `go ret.processAnimeJobs()` / `go ret.processMangaJobs()` (sync.go:87-88). Both mutate the maps under `q.queueStateMu.Lock()`: assignment at :97 and :122, `delete()` at :110 and :135.
2. Reader is unguarded. `func (q *Syncer) GetQueueState() QueueState { return q.queueState }` acquires nothing. The struct return copies the QueueState header but SHARES the map headers, so returning by value provides no isolation. `RespondWithData` then JSON-marshals it, and encoding/json ranges over AnimeTasks/MangaTasks — a genuine map read on the request goroutine.
3. HTTP path is live. routes.go:566 `v1Local.GET("/queue", h.HandleLocalGetSyncQueueState)`; handler local.go:159-160 calls GetQueueState() directly. Not dead/test-only.

Concurrent unsynchronized map read + write triggers the runtime's `fatal error: concurrent map read and map write`, which is a throw, not a panic — the `util.HandlePanicInModule*` defers elsewhere in this file cannot recover it. Whole process dies.

I found reachability is STRONGER than the finder argued, not weaker: `useLocalGetSyncQueueData` (seanime-web/src/api/hooks/local.hooks.ts:65-72, `enabled: true`) is invalidated in the onSuccess of useLocalAddTrackedMedia/useLocalRemoveTrackedMedia (local.hooks.ts:30, :46) — precisely the mutations that enqueue sync jobs. The refetch is causally coupled to the write burst rather than merely coincident, so the overlap is not a rare coincidence.

ADVERSARIAL CHECKS THAT FAILED TO KILL IT: no caller-side lock (handler takes none); no single-goroutine confinement (two writer goroutines + N request goroutines); the value-return is not a defense (shared map headers); the ws path is NOT the bug — SendQueueStateToClient's marshal at :102/:110/:127/:135 runs while queueStateMu.Lock() is held, so the two worker goroutines correctly exclude each other; only the HTTP reader is unsynchronized.

CORRECTION TO THE FINDER: the proposed fix is itself broken — see corrected_fix. Severity downgraded critical->high per corrected_severity.
- **Fix:** The finder's proposed fix ("acquire RLock inside GetQueueState") introduces a DETERMINISTIC DEADLOCK and must not be applied as written.

Why: SendQueueStateToClient() (sync.go:174-176) calls GetQueueState(), and SendQueueStateToClient is itself invoked at lines 102, 110, 127 and 135 *while the calling goroutine already holds queueStateMu.Lock()*. Go's sync.RWMutex is not reentrant: an RLock() taken by a goroutine that already holds the write lock blocks forever. Adding RLock to GetQueueState therefore hangs processAnimeJobs and processMangaJobs on their first job — trading a probabilistic crash for a guaranteed permanent freeze of all local sync.

Correct fix — split the locked and unlocked variants:

1. Add an unexported helper that assumes the lock is already held and returns an isolated snapshot:
     func (q *Syncer) snapshotQueueStateLocked() QueueState {
         s := QueueState{
             AnimeTasks: make(map[int]*QueueMediaTask, len(q.queueState.AnimeTasks)),
             MangaTasks: make(map[int]*QueueMediaTask, len(q.queueState.MangaTasks)),
         }
         for k, v := range q.queueState.AnimeTasks { s.AnimeTasks[k] = v }
         for k, v := range q.queueState.MangaTasks { s.MangaTasks[k] = v }
         return s
     }
2. GetQueueState() becomes the public, self-locking entry point:
     func (q *Syncer) GetQueueState() QueueState {
         q.queueStateMu.RLock()
         defer q.queueStateMu.RUnlock()
         return q.snapshotQueueStateLocked()
     }
3. Add sendQueueStateToClientLocked() that calls snapshotQueueStateLocked() and sends, and switch the four in-lock call sites (:102, :110, :127, :135) to it. Leave the call at :156 (checkAndUpdateLocalCollections, which holds q.mu but NOT q.queueStateMu) on the public SendQueueStateToClient/GetQueueState path.

Refinement over the finder: a SHALLOW map copy is sufficient — no deep copy of *QueueMediaTask needed. The task structs are constructed once at :97/:122 and thereafter only assigned or deleted, never field-mutated, so sharing the pointers across the lock boundary is race-free. Deep-copying the structs would be dead work.

Cheaper alternative if the copy is unwanted: move the four SendQueueStateToClient calls to after their Unlock() and have GetQueueState RLock+snapshot. This is a smaller diff but permits two workers to emit ws events out of enqueue order, so the sendQueueStateToClientLocked approach is preferable.
- **Regression risk:** 1. DEADLOCK (the dominant risk, and the reason the finder's fix must be corrected): naively adding RLock() to GetQueueState() self-deadlocks processAnimeJobs/processMangaJobs, because SendQueueStateToClient -> GetQueueState is called at sync.go:102, :110, :127, :135 with queueStateMu.Lock() already held by the same goroutine. Blast radius = 100% of local/offline sync silently freezes on the first queued job (worse than the bug being fixed, since the current crash is only probabilistic). The locked/unlocked split above avoids this.

2. Nested-lock ordering with q.mu: checkAndUpdateLocalCollections (:144-166) holds q.mu and calls SendQueueStateToClient at :156. Once SendQueueStateToClient acquires queueStateMu, the lock order q.mu -> queueStateMu is established there. No existing path takes queueStateMu then q.mu (the workers release queueStateMu at :104/:112/:129/:137 before calling checkAndUpdateLocalCollections at :114/:139), so no lock-order inversion today — but this ordering constraint is newly load-bearing and any future code taking queueStateMu before q.mu would deadlock.

3. Snapshot semantics change for the ws payload: SendQueueStateToClient currently hands the LIVE maps to WSEventManager.SendEvent, which marshals them inline inside conn.writeJSON (events/websocket.go:323-360) while the worker still holds the lock. After the fix it marshals an isolated copy. Client-observable output is identical; the only behavioral delta is that the ws payload is now frozen at snapshot time rather than at marshal time — strictly safer, and it also removes the latent hazard of a live map escaping into the ws layer.

4. Per-request allocation on GET /api/v1/local/queue: two extra maps per call. Negligible (queue is bounded by the 100-slot job channels) and the endpoint is low-frequency.

5. Serialization against workers: GetQueueState's RLock can now briefly block behind a worker's write Lock. The critical sections are a single map assign/delete plus (currently) a ws broadcast; if the ws send is left inside the lock, a slow ws client could stall the /queue handler. Using sendQueueStateToClientLocked keeps that broadcast inside the lock, so if handler latency matters, prefer moving the send outside the lock (alternative in corrected_fix). Note events/websocket.go:323-360 already snapshots m.Conns and writes outside m.mu specifically to avoid a slow client stalling ws process-wide (the F7 fix), so the exposure is bounded but not zero.

6. No API/type change: QueueState is exported and codegen'd to the frontend (codegen/generated/handlers.json, seanime-web Local_QueueState). The fix touches only locking/copying, not the shape, so no regeneration or client change is required.

#### PLUG-PLG-1 — goja.Runtime mutated from a second, unsynchronized goroutine in BindFetch — **open** (high, finder said critical)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** plugin-runtime
- **Location:** `internal/goja/goja_bindings/fetch.go:181-204,402-526 (called from internal/extension_repo/goja_plugin.go:178,196)`
- **What:** goja_bindings.BindFetch spawns a dedicated goroutine (`go func(){ for fn := range f.ResponseChannel() { fn() } }()`, fetch.go:192-200) that calls the promise's `resolve`/`reject` closures directly on `f.vm` from that goroutine. `f.vm` is not an ephemeral, single-owner runtime here: it is the plugin's long-lived pool-executor runtime (goja_plugin.go:178, created once per pooled runtime in the pool factory) and the persistent, shared `uiVM` (goja_plugin.go:196). uiVM is also driven synchronously by the WS-event goroutine (`ui.go` Register spawns `go func(){ for event := range ...{ u.HandleWSEvent(event) } }()` at ui.go:179-185, which calls into `u.vm` while holding only `u.mu.RLock()` — a non-exclusive lock). The fetch pump goroutine never takes `u.mu` at all, so it can call `resolve()`/`reject()` on `u.vm`/`uiVM` at the exact same time another goroutine is executing JS on that same runtime.
- **Why it matters:** goja.Runtime is explicitly documented as not safe for concurrent use. Two goroutines touching the same *goja.Runtime with zero mutual exclusion (fetch pump vs. WS-event dispatch, or fetch pump vs. a hook-executor goroutine reusing a pool runtime while a stale pending fetch from a previous checkout resolves) can corrupt VM internal state, causing sporadic panics, wrong values, or crashes — hard-to-reproduce heisenbugs under real plugin traffic (any plugin that calls network `fetch`).
- **Evidence:** fetch.go:192-200: `go func() { for fn := range f.ResponseChannel() { ... fn() } }()` — fn() invokes resolve()/reject() bound to f.vm. goja_plugin.go:196: `goja_bindings.BindFetch(ext.ID, uiVM, ...)` binds this pump to the persistent UI runtime. ui.go:179-185 spawns a second goroutine calling `u.HandleWSEvent(event)` which (ui.go:235) only takes `u.mu.RLock()` — no coordination with the fetch pump goroutine at all.
- **Verifier:** Verified in source, not paraphrase. fetch.go:192-201 pump calls fn() directly; fn bodies at fetch.go:415-422/501-503/522-525 call resolve/reject with f.vm, and toGojaObject (fetch.go:531-556) does vm.NewObject() + ~15 obj.Set() — heavy mutation from the pump goroutine. goja_plugin.go:178 binds this into the pool FACTORY (long-lived pooled runtime), :196 onto the persistent uiVM. The repo convicts itself: internal/plugin/ui/fetch.go:15-22 is the identical pump on the SAME runtime (ui.go:70 vm: options.VM -> context.go:127 vm: ui.vm, so c.vm == uiVM) but wraps fn() in c.scheduler.ScheduleAsync; scheduler.go:20-21 states the contract verbatim: "Any goroutine that needs to execute a VM operation must schedule it because the UI VM isn't thread safe". BindAbortContext(c.vm, c.scheduler) likewise takes a scheduler. BindFetch is the sole violator. Adversarial checks all failed to kill it: (1) No shadowing — plugin_ui sets "fetch" on the ctx obj (fetch.go:13), never the global, so BindFetch's global fetch on uiVM stays live alongside the safe ctx.fetch. (2) Not dead code — on pool runtimes (goja_plugin.go:178) the unsafe global fetch is the ONLY fetch; there is no ctx object and no scheduler at all. (3) Not serialized — the pool is a bare sync.Pool (goja_runtime_manager.go:193-218); Put only calls ClearInterrupt(), so a fetch still pending at Put resolves onto a runtime a different hook goroutine has since checked out. The pump is created once per runtime in the factory and never exits. (4) No effective guard — f.closed is checked at fetch.go:404 (before the HTTP call), never at resolve time; the recover at :194-198 is itself misplaced (defer inside a for loop accumulates to function exit, not per iteration). One correction to the finder's mechanism: it names ui.go:179-185/HandleWSEvent's u.mu.RLock() as the race partner, but HandleWSEvent mostly funnels into ScheduleAsync. The actual concurrent VM owner is the scheduler goroutine (scheduler.go:57-70, job.fn()). Same defect, stronger proof.
- **Fix:** Two distinct paths need distinct fixes; the finder's one-liner only covers one. (A) uiVM (goja_plugin.go:196): the cleanest fix is to DELETE the BindFetch call entirely — plugin_ui already binds a correct, scheduler-serialized fetch on the same VM (plugin/ui/fetch.go:13). Verify no plugin relies on the global-vs-ctx distinction first; if global fetch must stay, give BindFetch an optional scheduler and mirror plugin_ui/fetch.go's ScheduleAsync wrapper. (B) Pool runtimes (goja_plugin.go:178): there is NO scheduler here, so ScheduleAsync is not available. Each pooled runtime needs its own owning goroutine/serialization, plus resolution must be tied to the checkout lifetime — a fetch that has not settled by the time Pool.Put runs must be cancelled or drained, otherwise it resolves onto whoever holds the runtime next. Note the pool path is arguably already broken independent of the race: goja has no event loop there, so RunString returns before the pump ever fires and awaited fetches in hooks likely never settle within the invocation — worth confirming before investing in serialization for path B. Also fix the misplaced per-iteration defer/recover at fetch.go:194-198 while in there.
- **Regression risk:** Concrete, on a shared path used by every plugin that calls network fetch. (1) ScheduleAsync silently DROPS jobs when its 9999-deep queue is full (scheduler.go, default: branch — only reports via onException). Routing fetch resolution through it converts today's always-resolves behavior into a promise that never settles: the plugin hangs forever instead of racing. Today fn() runs unconditionally. (2) Lifecycle: the scheduler is stopped on unload (ScheduleAsync returns early on ctx.Done()), and ClearInterrupt (goja_plugin.go:85-121) stops store/storage/pool while the fetch pump keeps running — after the fix, in-flight fetches at unload would be silently discarded rather than resolved/rejected, which could leave awaiting plugin code wedged where it previously errored out cleanly. (3) Ordering: fetch resolutions currently interleave immediately; funnelling them behind DOM/tray/action renders (context.go, dom.go, action.go all ScheduleAsync onto the same queue) serializes them behind potentially slow render jobs, adding latency and changing observable resolution order for plugins that race multiple fetches. (4) Deleting the uiVM BindFetch (fix A) removes the GLOBAL fetch symbol from uiVM — any plugin UI code calling bare fetch(...) instead of ctx.fetch(...) breaks with a ReferenceError. Note the two bindings also differ in allowedDomains/anilistToken wiring (bindFetch passes SetAnilistToken, BindFetch does not), so they are not behaviorally identical. (5) goja_base.go:77 shares BindFetch for the non-plugin provider extensions; any signature change there must not alter that path's currently-working behavior.

#### PLUG-PLG-4 — Fetch pump goroutines and Fetch instances from goja_bindings.BindFetch are never closed on plugin unload — **open** (high, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** leak  |  **Slice:** plugin-runtime
- **Location:** `internal/extension_repo/goja_plugin.go:85-116 (ClearInterrupt), 175-183 (pool factory), 196 (uiVM); internal/goja/goja_bindings/fetch.go:153-160,181-204`
- **What:** `Fetch.Close()` (fetch.go:153-160) closes `vmResponseCh`, which is what makes the pump goroutine spawned in `BindFetch` (fetch.go:192-200) exit its `for fn := range ...` loop. Nothing in the plugin lifecycle ever calls it for the Fetch instances created via the raw `goja_bindings.BindFetch(ext.ID, runtime, ...)` calls at goja_plugin.go:178 (once per pool-prewarmed runtime, 5x per plugin) and :196 (uiVM). `GojaPlugin.ClearInterrupt()` (goja_plugin.go:85-116, invoked on unload) calls `p.loader.ClearInterrupt()`, `p.runtimeManager.DeletePluginPool(...)`, and `p.ui.Unload(...)`, but never `Fetch.Close()` for any of these bindings. By contrast, `plugin_ui.Context.bindFetch` (ui/fetch.go:9-28) correctly registers `f.Close()` via `c.registerOnCleanup`, showing the intended pattern was only applied to the safer, secondary fetch binding, not the primary global `fetch`.
- **Why it matters:** Every plugin load/unload cycle (dev iteration, user toggling a plugin, marketplace update) permanently leaks at least 6 goroutines (5 pool runtimes + uiVM) blocked forever on an unclosed channel — a slow, cumulative goroutine leak that grows with plugin reload frequency and is never reclaimed until process restart.
- **Evidence:** fetch.go:153-160 `func (f *Fetch) Close() { ... close(f.vmResponseCh) }`; no call site for it exists outside internal/plugin/ui/fetch.go:26, confirmed via grep across internal/extension_repo and internal/plugin for `.Close()` near fetch/Fetch.
- **Verifier:** Verified against source, and the finder understated it. (1) Fetch.Close() at fetch.go:153-160 is quoted accurately: it sets f.closed and closes vmResponseCh, the only thing that terminates the `for fn := range f.ResponseChannel()` pump spawned at fetch.go:192-201. (2) All three non-test BindFetch call sites DISCARD the returned *Fetch — goja_base.go:77, goja_plugin.go:178, goja_plugin.go:196 are bare statement calls with no assignment — so Close() is not merely un-called, the pointer is unreachable by construction. (3) GojaPlugin.ClearInterrupt (goja_plugin.go:85-121) does ui.Unload / loader.ClearInterrupt / store.Stop / storage.Stop / DeletePluginPool / hook-unbind, and no Fetch.Close. (4) newPool(5, ...) at goja_runtime_manager.go:44 confirms the 5-prewarm + uiVM = 6 count. (5) DeletePluginPool only calls runtime.ClearInterrupt() (which CLEARS an interrupt flag, it does not stop anything) then runtime.GC() — and that GC can reclaim nothing, because the blocked pump goroutine is a GC root holding f -> f.vm -> the entire goja Runtime. A goroutine blocked on an unclosed channel is never reclaimed by Go's GC. (6) The ui/fetch.go:24-27 contrast is real (registerOnCleanup -> f.Close()). Reachability is proven: ClearInterrupt fires from invalidateExtension (external.go:859), ReloadExternalExtensions (external.go:504), and the UI-crash watchdog (goja_plugin.go:213-217). TWO WAYS WORSE THAN CLAIMED: (a) goja_base.go:77 has the identical discarded-Fetch pattern, so the leak is not plugin-only — it hits JS providers (torrent/manga search), exercised far more often than plugin reloads; (b) Pool.Get (goja_runtime_manager.go:193-207) makes it unbounded with NO unload at all: sync.Pool drops its contents every GC cycle, so v==nil -> p.factory() -> new runtime -> new BindFetch -> new pump goroutine, while the orphaned prior runtimes stay alive rooted by their own goroutines. Each leaked unit is a full goja Runtime plus two cap-50 channels, not just an 8KB stack. The steady-state amplification is a strong inference from documented sync.Pool semantics rather than a measured result; the unload leak itself is airtight from the code alone.
- **Fix:** The finder's fix (retain *Fetch refs, call Close() in ClearInterrupt) is UNSAFE as written and also incomplete. Unsafe: f.closed is checked only once at fetch.go:404, BEFORE the HTTP request; the sends at :415, :501 and :522 all occur after it with no re-check, so closing vmResponseCh while a request is in flight is a send-on-closed-channel panic. It is recovered by the HandlePanicInModuleThen at :403 (no process crash), but the JS promise then never settles and any hook blocked in handlePromiseResult (goja_plugin.go:447-455) eats the full 30s timeout. Incomplete: runtimes minted by the Pool.Get factory-miss path (:203) are not tracked by anything the plugin holds, so a snapshot list closed at unload misses them and leaves the dominant leak in place. Correct fix, in three parts: (1) Give Fetch an explicit `done chan struct{}` closed by Close() instead of closing vmResponseCh directly; have the pump `select` on {fn := <-vmResponseCh, <-done}` and have every send site use `select { case f.vmResponseCh <- fn: case <-f.done: return }`. This terminates the pump with zero panic window and lets in-flight requests drop their result cleanly. (2) Make ownership explicit rather than retrofitting it: have the pool factory register each *Fetch into a mutex-guarded slice on GojaPlugin (covering both the 5 prewarms AND every later factory-miss creation), and close them all in ClearInterrupt alongside DeletePluginPool. Cleanest structural variant is to move ownership into goja_runtime.Pool itself so the pool closes the Fetch of every runtime it created. (3) Apply the same treatment to goja_base.go:77, which has the identical discarded-Fetch bug for JS providers and is the higher-traffic path.
- **Regression risk:** Three concrete risks, all on live paths. (1) Unload-during-active-hook becomes a 30s stall: m.Run (goja_runtime_manager.go:102-114) can be holding a pooled runtime and executing a plugin hook at the exact moment ClearInterrupt fires. Closing that runtime's Fetch mid-request means resolve/reject never runs, the goja promise never settles, and a caller in handlePromiseResult (goja_plugin.go:447-455) blocks for its full 30s WaitForPromise timeout. Today that request completes into a dead VM harmlessly. Mitigated only by the done-channel design plus rejecting rather than dropping in-flight promises. (2) Send-on-closed panic storm: with the finder's literal close(vmResponseCh) fix, every in-flight fetch at unload panics at :415/:501/:522. HandlePanicInModuleThen recovers each one, so no crash, but it turns a silent leak into WARN log noise on every plugin toggle — and the recover at :194-198 in the pump is itself already broken (the defer is inside the for loop, so it accumulates per iteration and only runs at goroutine exit, protecting nothing per-fn). Do not rely on that recover as a safety net. (3) Premature close via pool reuse: runtimes are returned to sync.Pool by Pool.Put (:212-219) and are NOT plugin-scoped at the type level. If a fix closes Fetch instances by iterating pool contents rather than by tracked creation, a runtime handed out to another caller could have its fetch binding killed underneath it, breaking currently-working provider/plugin network calls. Blast radius of the shared code path: BindFetch has 3 non-test callers (goja_base.go:77, goja_plugin.go:178, :196), so any signature or ownership change to it touches JS providers (torrent/manga search) as well as plugins — providers are the hotter path, and regressing them breaks search, not just plugin reload.

#### DEBR-DBG-1 — Truncated-file guard from 4ebfa601 never fires on the preload/prewarm play path (only the cold-play path) — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** inconsistency  |  **Slice:** debrid-directstream
- **Location:** `internal/debrid/client/stream.go:1702-1713 (playPreloadedStream's PlayDebridStreamOptions) vs stream.go:980-992 (cold startStream path)`
- **What:** Commit 4ebfa601 added a CDN-truncated-file guard: httpBaseStream.expectedSize is compared against the CDN's Content-Length in loadPlaybackInfo (internal/directstream/httpstream.go:343, via truncatedStreamErr), and it only trips when expectedSize > 0. The cold/direct play path in StreamManager.startStream correctly wires this: `ExpectedSize: knownFileSize(provider, torrentItemId, fileId)` at stream.go:988. But playPreloadedStream — the path that serves everything already sitting in the preload/prewarm cache (continue-watching, next-episode, cross-user DB-shared prewarms) — builds its directstream.PlayDebridStreamOptions at stream.go:1702-1713 with NO ExpectedSize field at all, even though `cached.torrentItemId` and `cached.fileId` are already in scope there (used two lines above for `resolveClientCdnUrl`). Since Go zero-values the field, `s.expectedSize` is always 0 for every stream opened via this path, so `truncatedStreamErr` always returns nil (expected<=0 short-circuits) regardless of what the CDN actually serves. Additionally, `preloadStreamWith` (stream.go:1216-1415), which does the actual background resolve that populates `s.preloads[key]`, calls `provider.GetTorrentStreamUrl` and stores the URL directly (stream.go:1370-1405) with zero size validation of any kind — no probe, no KnownFileSize check. Within playPreloadedStream itself, the only size-aware guard is `probeStreamURLWithSize` at stream.go:1660, and it is gated behind `!refreshed &&` — i.e. it is skipped entirely both (a) when the cached link hasn't hit its TTL yet (the common case: `if torrentItemId != "" && time.Since(urlResolvedAt) > urlRefreshTTL` at line 1625 is false, so the whole block including the probe is never entered), and (b) when the refresh succeeds and returns a URL (`refreshed=true` at line 1646, so `!refreshed` is false and the probe call at 1660 is skipped) — which is exactly the failure mode the commit's own description reports: 'the same torrent hands back the same rotten link' on re-resolve.
- **Why it matters:** This is the exact real-world bug the commit set out to fix (12s truncated playback from a torrent whose provider API still reports progress=1/cached=true), but the fix only covers the minority cold-resolve path. The preload/prewarm system is the primary path for continue-watching and next-episode clicks (per PrewarmStreams/preloadStreamBlocking scheduling), so a rotten cached link resolved by the background prewarm — or refreshed at play time — will silently play ~12s of video and stop, with zero detection, identical to the pre-fix behavior. Users hitting this bug via 'continue watching' or 'next episode' (the most common play trigger) get no protection at all.
- **Evidence:** stream.go:1702-1713: `err = s.ds(opts).PlayDebridStream(streamCtx, cached.filepath, directstream.PlayDebridStreamOptions{ StreamUrl: streamUrl, ClientStreamUrl: clientStreamUrl, MediaId: media.ID, AnidbEpisode: opts.AniDBEpisode, Media: media, Torrent: cached.torrent, FileId: cached.fileId, UserAgent: opts.UserAgent, ClientId: opts.ClientId, AutoSelect: false, })` — no ExpectedSize, contrast with stream.go:988 `ExpectedSize: knownFileSize(provider, torrentItemId, fileId),` in the cold path. httpstream.go:343 `if truncErr := truncatedStreamErr(s.contentLength, s.expectedSize); truncErr != nil {` combined with httpstream.go:150-156 `func truncatedStreamErr(served, expected int64) error { if expected <= 0 || served <= 0 || served == expected { return nil } ...}` confirms expectedSize==0 makes the guard permanently inert for this path. stream.go:1660 `if !refreshed && !probeStreamURLWithSize(ctx, streamUrl, prewarmProbeTimeoutPlay, s.knownFileSizeFor(torrentItemId, cached.fileId)) { return dropDeadPreload() }` shows the only size probe on this path is skipped whenever refreshed==true or the TTL hasn't expired.
- **Verifier:** All quoted code is real and current; nothing hallucinated. Verified: (1) internal/debrid/client/stream.go:980-992 cold path sets `ExpectedSize: knownFileSize(provider, torrentItemId, fileId)` at :988; playPreloadedStream's PlayDebridStreamOptions literal at :1702-1713 omits the field entirely. (2) `ExpectedSize` has exactly ONE assignment site feeding the guard — grep over internal/directstream + internal/debrid shows only debridstream.go:85 `expectedSize: opts.ExpectedSize` -> httpstream.go:343 `truncatedStreamErr(s.contentLength, s.expectedSize)`; truncatedStreamErr (httpstream.go:149-155) returns nil when `expected <= 0`. So Go's zero value makes the guard permanently inert for every stream opened via playPreloadedStream. No alternate wiring, nil-check, or caller-side validation exists. (3) The path is live, not dead code: StartStream dispatches to playPreloadedStream at stream.go:441 (in-memory preload hit) and :455 (hydratePrewarmFromDB cross-user/post-restart hit). (4) The probe-gating claim holds: probeStreamURLWithSize at :1660 is inside `if torrentItemId != "" && time.Since(urlResolvedAt) > urlRefreshTTL` (:1625) AND behind `!refreshed`, so a within-TTL cached link (the common case) gets no size check at all, and a successful refresh (:1646 sets refreshed=true) skips it too. KILL-ATTEMPT THAT FAILED: I tried to refute this by proving the proposed fix would be inert (knownFileSizeFor returning 0 on the preload path). It is not inert — knownFileSizeFor (:372-378) -> knownFileSize (:383-396) -> TorBox.KnownFileSize (torbox.go:198-207) reads fileIdCache, which is populated with the real size by storeFileId at torbox.go:633 inside GetTorrentStreamUrl — exactly the call preloadStreamWith makes to build the entry (same process, same provider instance). Line 1660 already calling s.knownFileSizeFor(torrentItemId, cached.fileId) on this same path is direct proof the value is available in scope here. Fail-open only when the provider isn't a FileSizeKnower or the 4096-cap cache was reset — which is safe, matching cold-path semantics.
- **Fix:** Add `ExpectedSize: s.knownFileSizeFor(torrentItemId, cached.fileId)` to the PlayDebridStreamOptions literal in playPreloadedStream (stream.go ~1702). Use the snapshotted local `torrentItemId` (taken under preloadMu at ~1585) rather than re-reading cached.torrentItemId, so the value is consistent with the refresh block above and with the URL actually being played. Drop the finder's second suggestion (running probeStreamURLWithSize on the refreshed==true branch) as redundant: once ExpectedSize is wired, loadPlaybackInfo's truncatedStreamErr already covers the refreshed link using the Content-Length it fetches anyway (httpstream.go:339 notes it "costs nothing"), whereas an extra probe adds a real HTTP round-trip to the latency-sensitive play path the whole preload system exists to make fast. Optional hardening (separate, lower priority): have preloadStreamWith record the provider-reported size on the preloadedDebridStream entry at resolve time, so DB-hydrated/cross-process prewarms keep the guard armed instead of silently failing open when fileIdCache has no entry.
- **Regression risk:** The fix widens a STRICT equality gate (`served != expected` -> hard abort, truncatedStreamErr only passes on served==expected) from the minority cold path to the primary preload/prewarm play path — continue-watching and next-episode clicks. Concretely: any case where TorBox's mylist-reported f.Size (torbox.go:626/540) legitimately disagrees by even one byte from the CDN's Content-Length currently plays fine via playPreloadedStream and would newly hard-fail the open. Candidate sources of benign disagreement: (a) fileIdCache is keyed "torrentID|shortName" (torbox.go:202), so a re-added torrent that reuses a torrentItemId but whose file content changed yields a stale-but-present size — cap-reset at 4096 (torbox.go:179-181) is safe (empty = fail open), but a stale mismatched entry is not; (b) provider-side size rounding/padding vs actual served bytes; (c) any CDN response whose contentLength is measured on a differently-encoded body. Blast radius is bounded and asymmetric in one respect worth noting: unlike the cold path, a failure here does NOT fall back — dropDeadPreload/errPreloadedLinkDead (stream.go:441/455) is only returned from the TTL/probe block ABOVE the play call, so a truncation abort raised inside PlayDebridStream returns plain `err` at ~1714 and surfaces as "Failed to play preloaded stream" with no cold re-resolve, where today the user would at least get 12s of video. That is the intended trade (fail loudly per the commit's own rationale, and httpstream.go:341 argues re-resolving does not help), but it means a false positive is a hard playback failure on the most common trigger, not a degraded one. Also note persistPrewarm (:1653) shares refreshed URLs account-wide, so a bad cached size could affect other users' hydrated entries — though those fail open (knownFileSize returns 0 on a cold fileIdCache), so the realistic downside is under-detection, not over-blocking.

#### TORR-TS-1 — currentTorrent/currentFile written without the client mutex while a background goroutine reads them under lock — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** torrentstream
- **Location:** `internal/torrentstream/stream.go:292-293 (unlocked write) vs internal/torrentstream/client.go:163-243 (locked read/write in the background status goroutine) and internal/torrentstream/stream.go:532-563 (a different write path that DOES take the lock)`
- **What:** Client.currentTorrent / Client.currentFile (mo.Option[*torrent.Torrent] / mo.Option[*torrent.File]) are shared fields guarded, in some code paths, by c.mu. The status-reporting goroutine spawned in initializeClient() (client.go) takes c.mu.Lock() before touching c.currentTorrent/c.currentFile every second (client.go:177-243), and the stop path in stream.go (handling media-player-stopped, ~line 532-563) also takes r.client.mu.Lock() before writing them. But the stream-start path that sets them when a new torrent begins playing writes directly without any lock: `r.client.currentFile = mo.Some(torrentToStream.File)` / `r.client.currentTorrent = mo.Some(torrentToStream.Torrent)` (stream.go:292-293). Several other reader methods (GetStreamingUrl, readyToStream, cleanupActiveTorrentFiles, dropExcessTorrents) also read these fields with no lock at all.
- **Why it matters:** This is a genuine, unsynchronized concurrent read/write on non-atomic struct fields (mo.Option wraps a pointer + bool) from multiple goroutines — the classic Go data race the race detector flags. A torn read in the 1s-interval background loop can observe an inconsistent Option (e.g., IsPresent() true with a stale/garbage pointer, or the reverse), leading to a nil deref via MustGet(), a stats panic, or the status loop silently reporting/acting on the previous torrent while playback has already moved to a new one.
- **Evidence:** stream.go:292-293: `r.client.currentFile = mo.Some(torrentToStream.File)` / `r.client.currentTorrent = mo.Some(torrentToStream.Torrent)` — no lock. client.go:177-180 (inside the ticking background goroutine): `c.mu.Lock(); if c.torrentClient.IsPresent() && c.currentTorrent.IsPresent() && c.currentFile.IsPresent() { t := c.currentTorrent.MustGet(); f := c.currentFile.MustGet() ... }` — same fields, under lock. stream.go:532,558-559 (stop path): `r.client.mu.Lock() ... r.client.currentTorrent = mo.None[*torrent.Torrent]() ... r.client.currentFile = mo.None[*torrent.File]()`.
- **Verifier:** The quoted code is real and current. stream.go:292-293 writes r.client.currentFile/currentTorrent holding only r.streamActionMu (acquired at stream.go:158); the background goroutine spawned in initializeClient (client.go:155, reached via repository.go:219) reads the same fields under c.mu.Lock() at client.go:177-180 on a 1s `default:`-case loop that runs for the process lifetime. StartStream runs on an HTTP handler goroutine and nothing serializes it against that loop — the reader's c.mu excludes nothing because the writer never takes it. This is an unsynchronized read/write on non-atomic struct fields (mo.Option = {isPresent bool; value T}), i.e. a textbook Go data race the detector would flag. The finder's supporting claims also check out: the stop path (stream.go:532-563) DOES take r.client.mu, and the unguarded readers exist (GetStreamingUrl client.go:266/282, readyToStream 690, cleanupActiveTorrentFiles 588, dropExcessTorrents 643, plus handler.go:29 on every stream request). The finder additionally MISSED a second unlocked writer: Shutdown() at client.go:495. Where the finder overreaches is impact: the "torn read -> MustGet() returns nil -> nil deref panic" requires None->Some store reordering (architecturally possible on the arm64 Pi, but not the common case), while the realistic interleaving is that 292 and 293 are two separate field writes, so a reader landing between them pairs the NEW file with the OLD torrent for one stats tick — a wrong percentage/speed number for <=1s. ResetBaselines() at stream.go:294 takes c.mu microseconds later, which does not remove the race but shrinks the exposure window to near-nothing. Real defect, materially smaller blast radius than claimed.
- **Fix:** Do NOT apply the finder's fix as written — it self-deadlocks. sync.Mutex is not reentrant, so wrapping stream.go:292-296 in c.mu.Lock() deadlocks on ResetBaselines() (client.go:751-753, called at line 294), and adding a lock inside cleanupActiveTorrentFiles deadlocks against client.go:238, which calls it while already holding c.mu. The codebase's implicit convention is callee-does-not-lock/caller-does (see dropTorrents: called under c.mu from client.go:152, writes currentTorrent unlocked at 495). Minimal correct fix: guard ONLY the two writes, before the ResetBaselines call, e.g. `r.client.mu.Lock(); r.client.currentFile = mo.Some(torrentToStream.File); r.client.currentTorrent = mo.Some(torrentToStream.Torrent); r.client.mu.Unlock()` at stream.go:292-293, leaving 294-296 outside the critical section. That alone kills the race with the status goroutine and makes the file/torrent pair update atomically. Also add the same guard to the second unlocked writer Shutdown() (client.go:495). The cleaner structural fix is to collapse both fields into one `atomic.Pointer[currentStream]` holding an immutable {Torrent, File} snapshot, which makes the pair inherently atomic and fixes every unlocked reader (handler.go:29, GetStreamingUrl, readyToStream, dropExcessTorrents) at once without touching the lock discipline — but that is a wider refactor and should be scoped separately. If only one change is made, do the two-line guard at 292-293.
- **Regression risk:** Concrete and non-trivial. (1) Deadlock: the naive fix (lock spanning stream.go:292-296) hard-deadlocks the streaming start path because ResetBaselines() at line 294 takes the same non-reentrant c.mu; likewise "add the lock to the unguarded readers" deadlocks cleanupActiveTorrentFiles, which client.go:238 already calls while holding c.mu. This would break torrent playback start entirely — a working path today. (2) Lock-ordering/hold-time: the status goroutine holds c.mu across expensive anacrolix calls (t.Stats(), t.PeerConns(), and cleanupActiveTorrentFiles at 237-239, which does file I/O). Making the start path block on c.mu means a stream start can now stall behind that loop's I/O for the length of a cleanup pass — new latency on the "Sending stream to media player" step, which the loading-screen work already tuned. (3) Blast radius if readers are locked: handler.go:29 reads currentFile on EVERY HTTP range request during playback; routing those through the same mutex the 1s stats loop holds during file cleanup would serialize video byte-serving behind stats work — a plausible new stutter/buffering source on the Pi. Keeping the fix to the two writes only (and Shutdown) avoids all three.

#### PLAY-MC-3 — videocore.VideoCore.Shutdown() leaks its internal effects/insight subscriber goroutines on every session eviction — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** leak  |  **Slice:** playback-plane
- **Location:** `internal/videocore/videocore.go:171-183 (Shutdown) vs internal/videocore/effects.go:17-53 (setupOnlinestreamEffects) and internal/videocore/insight.go:120-158 (InSight.Start)`
- **What:** VideoCore.Start() calls `vc.setupEffects()` which subscribes internally as "videocore:onlinestream" (effects.go:18) and InSight.Start() subscribes as "insight" (insight.go:121), each starting a `for e := range subscriber.Events() {...}` goroutine. VideoCore.Shutdown() (videocore.go:171-183) only closes `dispatcherStop` and unsubscribes the client-facing WS event stream — it never closes entries in `vc.subscribers`, so both of these internal subscriber goroutines block forever once the dispatch loop stops producing events.
- **Why it matters:** Same leak class as MC-2, in the sibling package, on the same session-eviction path (internal/core/session.go shutdown() calls `s.videoCore.Shutdown()` for every evicted user session). Two goroutines leak per session teardown instead of one.
- **Evidence:** videocore.go Shutdown(): `close(vc.dispatcherStop); if vc.wsEventManager != nil { vc.wsEventManager.UnsubscribeFromClientEvents(...) }` — no `vc.subscribers.Range(...)` close, so `setupOnlinestreamEffects`'s and `InSight.Start`'s `for e := range subscriber.Events()` loops (effects.go:21, insight.go:123) never terminate.
- **Verifier:** Read all cited spans; the finder's quotes are accurate and current. videocore.go:171-183 Shutdown() closes only dispatcherStop and calls UnsubscribeFromClientEvents("videocore:u%d") — there is no subscribers.Range close pass. Two internal subscribers are created and never removed: effects.go:18 vc.Subscribe("videocore:onlinestream") with `for e := range subscriber.Events()` at line 21 (started via Start()->setupEffects(), videocore.go:166), and insight.go:121 is.vc.Subscribe("insight") with `for event := range sub.Events()` at line 123 (started from New(), videocore.go:123). The only closers of a Subscriber.eventCh are Unsubscribe (videocore.go:280-287) and RegisterEventCallback's deferred cancel (303-306); grep of Unsubscribe callers in the package (adapter.go:171 + the two internal ones) shows nothing ever unsubscribes the "videocore:onlinestream" or "insight" ids. Since Subscribe only inserts into vc.subscribers and dispatchEvent stops producing after dispatcherStop closes, both range loops park forever and their closures retain the whole *VideoCore (incl. inSight caches). Reachability proven: internal/core/session.go:564-580 UserSession.shutdown() calls s.videoCore.Shutdown(); invoked from evictSession (553-558) on AniList relink (line 524), logout (547), and token-invalid — per-session VideoCore is built at session.go:132. Decisive counter to any "intentional" defense: the sibling internal/mpvcore/mpvcore.go:123-142 Shutdown() already performs exactly the missing pass (subscribers.Range -> closed.Store(true) + closeOnce.Do(close(sub.eventCh))) with the comment "Close internal subscribers (e.g. InSight) so their consumer goroutines return too." VideoCore's Subscriber has the identical shape (isClosed atomic.Bool + closeOnce) and simply omits it — a genuine unfixed parity gap.
- **Fix:** Mirror mpvcore.Shutdown()'s pass at the end of videocore.VideoCore.Shutdown(): vc.subscribers.Range(func(id string, sub *Subscriber) bool { sub.isClosed.Store(true); sub.closeOnce.Do(func() { close(sub.eventCh) }); return true }). Note the field is isClosed (not `closed` as in mpvcore). Ideally also drain/Clear the map afterwards. To avoid widening the pre-existing send-on-closed-channel race with dispatchEvent, the sounder variant is to have the effects/insight goroutines select on vc.dispatcherStop alongside their range (or have dispatchEvent hold a per-subscriber lock / re-check isClosed under the same mutex that Shutdown sets it), rather than only closing the channels.
- **Regression risk:** The proposed fix closes channels that dispatchEvent (videocore.go:205-225) sends into. dispatchEvent checks subscriber.isClosed.Load() and then sends, with no synchronization between the check and the send — so a Shutdown racing a live dispatch can close eventCh between the check and `case subscriber.eventCh <- event:`, producing a "send on closed channel" panic that takes down the whole process. That window exists today via Unsubscribe (and is exercised by GetTextTracks/GetPlaylist/GetSkipData/PullStatus, which all RegisterEventCallback + defer cancel while events flow), but the fix makes eviction-time Shutdown a new, wider trigger — closing every subscriber at once, including any RegisterEventCallback subscriber still mid-dispatch. Second, non-fatal risk: closeOnce guards double-close, so RegisterEventCallback's deferred Unsubscribe after Shutdown is safe; and the App-global VideoCore (internal/core/modules.go:238) never has Shutdown called outside tests, so onlinestream/insight effects on the admin instance are unaffected. Blast radius of Shutdown itself is small: callers are session.go:578, directstream/stream_test.go:115, videocore_test.go:123.

#### CORE-EVT-2 — RemoveConn deletes by client-ID (first slice match) instead of by connection identity, and AddConn never evicts a stale same-ID entry — ghost connections keep watch-rooms alive forever — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** core-events
- **Location:** `internal/events/websocket.go:167-184 (AddConn), 294-303 (RemoveConn) — no dedup/eviction on ID collision; contrast internal/nakama/host.go:227-243 and internal/nakama/peer.go:460-490 which DO set SetReadDeadline+SetPongHandler`
- **What:** AddConn always appends a new *WSConn without checking whether a connection with the same `id` is already registered. RemoveConn(id) walks m.Conns and removes only the FIRST entry whose ID matches — by string equality, not by the specific *websocket.Conn instance that actually disconnected. The persisted client-identity design (see 'Client identity restart bug') means a client reuses the SAME id across ordinary reconnects (page reload, sleep/wake, flaky network), so this is the normal path, not an edge case. Unlike internal/nakama's websocket handling (host.go/peer.go), the main event-plane handler (internal/handlers/websocket.go) never calls SetReadDeadline/SetPongHandler, so a stale connection can sit unnoticed in m.Conns for a long time (or indefinitely on a silent network drop) before its ReadMessage() call ever errors and triggers RemoveConn — widening the window where two entries share one ID.
- **Why it matters:** internal/nakama/watch_room.go's idle-room reaper (reapIdleRoomsWith, ~line 1298) determines room liveness purely from `wsEventManager.GetClientIds()` (fed directly by m.Conns): `if _, ok := live[p.ClientID]; ok { hasLive = true; room.lastLiveAt = now }`. If a ghost/duplicate WSConn entry for a departed participant's client ID is never removed (because a same-ID RemoveConn call hit the wrong slice entry, or the stale conn's read loop hasn't errored yet), that room is considered live on every reaper tick forever and is never cleaned up — directly contradicting the reaper's own doc comment: 'a room whose members all vanish without leaving would otherwise linger forever.' It can also cause duplicate event delivery to a client while both entries exist (SendEventTo/SendEvent write to every matching/broadcast conn).
- **Evidence:** AddConn: `m.Conns = append(m.Conns, &WSConn{ID: id, ...})` — no prior lookup/eviction. RemoveConn: `for i, conn := range m.Conns { if conn.ID == id { m.Conns = append(m.Conns[:i], m.Conns[i+1:]...); break } }` — removes by ID, first match, regardless of which physical conn actually closed. reapIdleRoomsWith: `for _, id := range h.manager.wsEventManager.GetClientIds() { live[id] = struct{}{} } ... if _, ok := live[p.ClientID]; ok { hasLive = true; ... continue }`.
- **Verifier:** The quoted code is real and current. internal/events/websocket.go:167-184 AddConn appends a *WSConn with no lookup for an existing same-ID entry; :294-303 RemoveConn(id) removes the FIRST slice entry matching by string ID and breaks, with no reference to which *websocket.Conn actually closed. The only callers are internal/handlers/websocket.go:67 (AddConn) and :91 (RemoveConn(id)) — the handler holds the exact `ws` instance but passes only the bare id, so removal-by-identity is impossible. No dedup/eviction exists anywhere (grep over all AddConn/RemoveConn call sites; only websocket_test.go otherwise). The nakama contrast is accurate: host.go:227-243 and peer.go:460-468 set SetPongHandler + SetReadDeadline(60s); the event-plane handler sets neither, so a stale conn is only detected when ReadMessage() eventually errors.

ID collision is not an edge case — it is guaranteed. seanime-web/src/lib/server/client-id.ts:30 persists clientId in **localStorage** (only the proof uses sessionStorage), so the id is shared by every tab of the same browser and stable across reloads. Multi-tab is an explicitly supported scenario (main-tab-claim broadcast at handlers/websocket.go:128, seanime-web/src/hooks/use-main-tab.ts). SendEventToIfOwner:285's own comment ("clientId is unique — stop at the first match") documents an assumption the client model violates.

However, the finder's HEADLINE impact — rooms kept alive forever by a ghost — is refuted. reapIdleRoomsWith (watch_room.go:1298-1322) is quoted correctly, but neither scenario produces an unbounded ghost: (a) in the multi-tab case a genuinely live conn shares the ID, so the room is legitimately live; (b) in the stale-conn case the ghost's read loop is still blocked in ReadMessage() and errors once TCP keepalive fails (listener-derived conns enable keepalive by default), firing a second RemoveConn that clears it. Bounded to minutes and self-healing, not "forever".

The real defect, which the finder buried as a secondary note, is inverted-victim removal: with tabs T1 and T2 sharing id X, closing T2 fires RemoveConn("X") which deletes T1's LIVE conn (index 0). T1's socket stays open and its read loop keeps running, but it is gone from m.Conns, so SendEvent/SendEventTo silently stop reaching a still-open tab (playback signaling, room state) with no error logged, until the user reloads. Duplicate delivery while both entries coexist is also real.
- **Fix:** Do ONLY the identity-based removal half of the proposed fix: have AddConn return the *WSConn it created (or a per-connection token), and have internal/handlers/websocket.go:91 call RemoveConn(thatConn) so cleanup targets the exact socket that closed by pointer identity, not by string ID. This alone fixes both the wrong-victim eviction and the stale-entry ghost.

Do NOT apply the "AddConn evicts/closes any existing same-ID entry" half — clientId is localStorage-scoped and therefore shared by concurrent tabs, so eviction would close a legitimately live tab's socket every time a second tab opens; both tabs auto-reconnect, so they would flap indefinitely, each reconnect killing the other. That converts a transient bug into a permanent one.

Treat the SetReadDeadline/SetPongHandler suggestion as a separate, optional change and only with a paired server-side ping ticker: the handler never sends protocol-level pings (the "ping" at line 110 is an app-level JSON message the client initiates), so adding a bare 60s read deadline would disconnect every idle client that happens not to send traffic.
- **Regression risk:** Two concrete risks, both on the AddConn-eviction half of the finder's proposal (which is why I dropped it): (1) Because client-id.ts:30 stores clientId in localStorage, concurrent tabs legitimately share one id. Evicting/closing the prior same-id socket on AddConn would tear down a working tab's event plane whenever a second tab opens; since websocket-provider auto-reconnects, two tabs would ping-pong-kill each other's sockets forever — breaking currently-working multi-tab use (main-tab-claim / use-main-tab.ts exist precisely because multi-tab is supported). (2) Adding SetReadDeadline without a server ping ticker would drop every idle client at the deadline — the event-plane handler never sends protocol pings (unlike nakama host.go:265/peer.go, which refresh the deadline from their own ping loops), so idle browser/Denshi/Tenji clients would be disconnected on a timer.

The narrow identity-based-RemoveConn fix I recommend has a much smaller blast radius: RemoveConn has exactly one non-test caller (handlers/websocket.go:91), and AddConn one (:67), so the signature change is contained; internal/events/websocket_test.go:14-15 calls AddConn("web-client", nil) and would need updating if AddConn's return is used. Behavior change to watch: correct removal means a departed participant's id genuinely leaves GetClientIds(), so reapIdleRoomsWith (watch_room.go:1298) will now actually reap rooms it previously kept alive via a ghost — desirable, but it makes room reaping newly reachable on a path that has been effectively inert, so roomIdleTTL behavior deserves a live check.

#### LIBR-PLAYLIST-1 — playlist.Manager mixes locked and unlocked mutation of shared playback state across goroutines — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** library-local
- **Location:** `internal/playlist/manager.go: resetPlaylist :535-541, StopPlaylist :696-729, session goroutine event handlers :302-415 (esp. :305 m.ctx.Done(), :370 m.currentEpisode=...) — contrast with PlayEpisode :733-781 and startPlaylist :272-291 which take m.mu`
- **What:** `m.mu` is only acquired in startPlaylist/PlayEpisode/ReopenEpisode. But the goroutine spawned inside startPlaylist (handling playbackManager/mediacore events) reads `m.ctx.Done()` directly off the struct every loop iteration, reassigns `m.currentEpisode = mo.Some(actualEpisode)` (line 370), and calls `m.markCurrentAsCompleted()`/`m.playNextEpisode()`/`m.StopPlaylist()` — none of these take m.mu. `resetPlaylist()` (mutates currentPlaylistData, currentEpisode, cancel) and `StopPlaylist()` (reads/calls m.cancel, reads currentPlaylistData) also never lock. So the exact same fields (`currentPlaylistData`, `currentEpisode`, `cancel`, `ctx`) are protected by m.mu on the client-request path (PlayEpisode) but freely mutated without it on the internal player-event path.
- **Why it matters:** A client 'play next episode' request (locked) can interleave with the session goroutine's own event-driven advance/stop (unlocked) on the same mo.Option fields — a genuine data race under Go's memory model. Concretely: a VideoCompletedEvent-triggered `m.playNextEpisode()`/`StopPlaylist()` racing a user-initiated `PlayEpisode('next')` can read a torn/stale `m.currentEpisode`, cause a double-advance, or leave the playlist manager's episode pointer inconsistent with what actually started playing.
- **Evidence:** func (m *Manager) resetPlaylist() {
	m.playbackManager.SetPlaylistActive(false)
	m.currentPlaylistData = mo.None[*playlistData]()
	m.currentEpisode = mo.None[*anime.PlaylistEpisode]()
	m.cancel = nil
	...
} // no m.mu.Lock(), called from unlocked `case <-m.ctx.Done():` (line 307) and unlocked StopPlaylist (line 723)
vs PlayEpisode: `m.mu.Lock(); defer m.mu.Unlock(); ... m.playEpisode(episode)` which sets the same `m.currentEpisode` field.
- **Verifier:** The core mechanism is real and I verified every load-bearing line in internal/playlist/manager.go (read directly, not paraphrased).

LOCK DISCIPLINE MAP (grep of `m.mu.*` + all `func (m *Manager)`):
- Take m.mu: startPlaylist(:273), playNextEpisode(:435), PlayEpisode(:734), ReopenEpisode(:784).
- Mutate the SAME fields with NO lock: resetPlaylist(:535-541 — writes currentPlaylistData, currentEpisode, cancel), StopPlaylist(:696-729 — reads m.cancel/calls it :698, reads currentPlaylistData :701, calls resetPlaylist :723), markCurrentAsCompleted(:496 — reads currentPlaylistData/currentEpisode), and the session goroutine's `m.currentEpisode = mo.Some(actualEpisode)` (:370) plus unlocked reads at :305 (m.ctx.Done()), :339, :354.

CONCURRENCY IS REAL, NOT SERIALIZED — three distinct goroutines with no ordering between them:
1. listenToEvents goroutine: ClientEventStop → StopPlaylist (:257, UNLOCKED); ClientEventPlayEpisode → PlayEpisode (:261, LOCKED); also reads m.cancel unlocked at :237 while startPlaylist writes m.ctx/m.cancel under lock at :291.
2. session goroutine spawned at :302: writes m.currentEpisode unlocked at :370, calls StopPlaylist (:326/:367/:410) and markCurrentAsCompleted+playNextEpisode (:329-330/:392-401).
3. `go m.startPlaylist(...)` at :252.
Because one side takes m.mu and the other doesn't, the mutex provides zero mutual exclusion for these fields. The struct (:97-128) confirms `mu sync.Mutex` and that currentPlaylistData/currentEpisode (mo.Option = 2-word {value, isPresent}), ctx, cancel are PLAIN non-atomic fields — while state/playerType/isLoadingNextEpisode ARE atomic.Value/atomic.Bool. The author clearly reasoned about concurrency here and simply left these four fields unguarded. A torn/stale mo.Option read is a genuine race under Go's memory model. resetPlaylist can even race with itself (reachable from both the ctx.Done branch :307 and StopPlaylist :723).

WHERE THE FINDER IS WRONG (does not rescue the finding): it claims playNextEpisode does not take m.mu — FALSE, it locks at :435-436. Every other cited line is accurate.

I checked callers: `grep` for StopPlaylist/PlayEpisode across all non-test .go files returns NOTHING outside manager.go. So this is not externally reachable API — but that doesn't refute it, since the two in-file goroutines are themselves concurrent and are the actual drivers.

BONUS DEFECT THE FINDER MISSED, same site: markCurrentAsCompleted(:496) has a stray orphan `m.mu.Unlock()` at :501 on the `currentPlaylistData` == None branch, and the function never locks. From the session goroutine (:329/:392/:400) that is an unlock of an unheld mutex → `fatal error: sync: unlock of unlocked mutex`, unrecoverable, kills the whole server process. From PlayEpisode (:740, which holds the lock) it unlocks mid-critical-section, then PlayEpisode's `defer m.mu.Unlock()` (:735) double-unlocks → same fatal. Client-triggerable by sending playEpisode{isCurrentCompleted:true} with no active playlist.
- **Fix:** Do NOT apply the finder's proposed fix as written ("take m.mu around every mutation, including inside StopPlaylist, resetPlaylist, and each branch of the event switch"). sync.Mutex is non-reentrant, so that change self-deadlocks immediately on existing paths: PlayEpisode holds mu (:734) and calls markCurrentAsCompleted (:740); playNextEpisode holds mu (:435) and calls playEpisode (:473) and prepareNextEpisode (:475); StopPlaylist calls resetPlaylist (:723). Locking the callees deadlocks the playlist on the very first "next episode".

Correct shape — split locked entry points from unlocked internals:
1. Rename the internals to explicit lock-held helpers: resetPlaylistLocked, markCurrentAsCompletedLocked, playEpisodeLocked, prepareNextEpisodeLocked, stopPlaylistLocked. Document "caller must hold m.mu".
2. Give StopPlaylist a thin exported wrapper that takes m.mu and delegates to stopPlaylistLocked. Same for the session goroutine's ctx.Done branch (:307) → lock, then resetPlaylistLocked.
3. Fix the :370 write and the :339/:354 reads by wrapping that whole VideoStartedEvent/StreamStartedEvent branch in m.mu.Lock()/Unlock() (not a defer — it sits inside a `for`/`select`, so use an explicit unlock or a closure, and mind the `continue` at :341/:368 which would skip an unlock).
4. DELETE the stray `m.mu.Unlock()` at :501 — it is an unconditional crash, and it must go before/with any lock-discipline change, otherwise the refactor converts it from one fatal into a different one.
5. Snapshot m.ctx into a local at the top of the session goroutine instead of re-reading m.ctx each loop iteration (:305). This fixes both the race with startPlaylist's `m.ctx, m.cancel = ...` (:291) AND a latent logic bug: after a second startPlaylist, the OLD goroutine begins observing the NEW context and never exits — a goroutine + subscriber leak.
The finder's alternative (single-owner goroutine + command channel) is the structurally right answer and matches the actor-per-room direction already recorded for nakama, but it is a rewrite; the locked/unlocked split is the surgical fix.
- **Regression risk:** Concrete, named risks on this exact path:

1. DEADLOCK (highest risk, and the finder's proposed fix causes it). The three call chains above (PlayEpisode→markCurrentAsCompleted, playNextEpisode→playEpisode/prepareNextEpisode, StopPlaylist→resetPlaylist) all cross the lock boundary today precisely BECAUSE the callees are unlocked. Adding locks without the Locked-suffix split hard-hangs playlist advance — a fully working user-visible feature (auto-play next episode) dies instantly.

2. Lock held across blocking I/O → stalls. startPlaylist already holds mu for its entire body (:273-420) including sendCurrentPlaylistToClient and mediacore/playbackManager subscribe calls. Widening locking to StopPlaylist/resetPlaylist means StopPlaylist can now block behind a slow startPlaylist. playEpisode (:543-607) calls m.playbackManager.Cancel(), mediacoreCoordinator.GetActiveSession/Terminate, and StartPlayingUsingMediaPlayer under the lock. If the session goroutine must now take mu to service player events, and the holder is blocked inside mpv/debrid startup, player events (LoadedMetadata/Completed/Terminated) queue up — which would resurface the documented "flipChatter / buffering storm" and MpvCore startup-serialization symptoms. Prefer locking narrowly around the field reads/writes, not around playbackManager/mediacore calls.

3. Deleting the :501 Unlock changes live behavior. Today, markCurrentAsCompleted-with-no-playlist either crashes the process or (from PlayEpisode) silently drops the lock so the rest of PlayEpisode runs unlocked. Removing it makes PlayEpisode correctly hold the lock through :743-780. If any callee down that path is discovered to re-enter, the crash converts to a hang — verify with `go test -race` plus an actual stop→playEpisode(isCurrentCompleted=true) sequence.

4. Snapshotting m.ctx (fix #5) changes shutdown semantics: the old goroutine will now correctly exit on the old context. Anything implicitly relying on the current buggy "old goroutine adopts the new ctx" behavior — specifically the UnsubscribeFromPlaybackStatus("playlist-manager") / mediacoreCoordinator.Unsubscribe("playlist-manager") calls at :308-311, which use a SHARED string key — could now double-unsubscribe or unsubscribe the NEW session's subscriber when the old goroutine exits. That shared-key subscriber is a real hazard: check Subscribe/Unsubscribe semantics for the "playlist-manager" key before landing this, or the fix silently kills playback signalling for the new playlist (the same class of failure as the v3.9 nil-Coordinator regression).

5. Blast radius is otherwise contained: grep confirms StopPlaylist/PlayEpisode/playNextEpisode have zero callers outside internal/playlist/manager.go, so no other package's behavior changes. Testing requires a real Denshi/system-player run — the race window only opens when player events and client events arrive together, which typecheck and unit tests will not surface.

#### PLUG-PLG-2 — No execution timeout/interrupt is ever enforced on plugin JS — DeletePluginPool 'interrupt' actually calls ClearInterrupt() — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** plugin-runtime
- **Location:** `internal/goja/goja_runtime/goja_runtime_manager.go:59-94,104-116`
- **What:** `Manager.DeletePluginPool` logs 'Interrupting all runtimes' and iterates idle pooled runtimes calling `runtime.ClearInterrupt()` (line 84) — the exact opposite of goja's `Interrupt(v)`, which is the only API that can preempt a running VM. Across the whole codebase (`internal/goja`, `internal/extension_repo`, `internal/plugin`), `runtime.Interrupt(...)` is never called anywhere — only `ClearInterrupt()` is (verified via grep across all non-test files). `Manager.Run`/`RunShared` also execute plugin hook code with `context.Background()` (goja_plugin.go:368) with no companion goroutine that calls `Interrupt()` on timeout/cancellation.
- **Why it matters:** A plugin with a JS infinite loop (or any pathologically slow synchronous code) permanently occupies the goroutine that invoked it and the checked-out goja.Runtime, and cannot be stopped even by unloading/disabling the plugin — DeletePluginPool's loop only touches runtimes currently idle in the sync.Pool (a checked-out, running runtime isn't there to be found), and even if it were, ClearInterrupt() would not stop it. This is a real DoS: any plugin bug or malicious plugin can permanently wedge a goroutine and leak a pool slot with no recovery path short of a process restart.
- **Evidence:** goja_runtime_manager.go:83-85: `// Interrupt the runtime` followed by `runtime.ClearInterrupt()`. goja_plugin.go:368: `p.runtimeManager.Run(context.Background(), p.ext.ID, func(executor *goja.Runtime) error {...})` — no timeout, no watchdog calling Interrupt.
- **Verifier:** Code is real and current, not paraphrased. goja_runtime_manager.go:83-85 literally reads `// Interrupt the runtime` / `runtime.ClearInterrupt()`, and the enclosing loop logs "Interrupting all runtimes"/"Interrupted %d runtimes" (68, 88). go.mod:21 pins stock github.com/dop251/goja with no replace directive (only anacrolix/torrent is replaced), so ClearInterrupt() has stock semantics: it CLEARS a pending interrupt; Interrupt(v) is the only API that preempts a running VM. A grep for `.Interrupt(` minus ClearInterrupt across internal/ returns zero hits — Interrupt is never called anywhere in the codebase; every one of the 18 hits is ClearInterrupt (incl. ui.go:107, commented `// Stop the VM`, which stops nothing). goja_plugin.go:368 confirmed: `p.runtimeManager.Run(context.Background(), ...)` with no deadline and no watchdog; Manager.Run (104-116) only threads ctx into pool.Get, never into fn, so plugin JS runs unbounded. Reachable: plugins are a shipped feature (NewGojaPlugin, extension_playground, external.go), hooks dispatch synchronously on request paths via reflect.MakeFunc handlers (358-396). So a JS infinite loop in a hook permanently wedges the invoking goroutine with no preemption path — CONFIRMED.

BUT two of the finder's impact claims are wrong and I refute them: (1) "leak a pool slot" / pool exhaustion is false. Pool.Get (193-209) calls p.factory() whenever sp.Get() returns nil (sp.New is hardwired to return nil at 177-179); sync.Pool is not a bounded semaphore and `size` is used only for prewarming. Get never blocks and never starves — the cost is one leaked runtime's memory plus the hung goroutine, nothing more. (2) "cannot be stopped even by unloading/disabling" is only half true: ClearInterrupt (85-121) unbinds all hooks (115-117), so unloading DOES stop future invocations; only already-running loops stay wedged. Also, the finder's cited precedent is itself illusory: handlePromiseResult (447-455) comments "force stop after 30 seconds" but calls WaitForPromise, which (internal/util/goja/async.go:21-31) merely polls promise.State() every 10ms and returns ctx.Err() — it abandons the wait while the JS keeps running. That is not a preemption pattern to mirror.
- **Fix:** The finder's proposed fix is partly ineffective. Interrupting runtimes inside DeletePluginPool's drain loop accomplishes nothing even when spelled Interrupt(): the loop can only reach runtimes sitting IDLE in the sync.Pool, and an idle runtime is by definition not executing JS — the wedged one is checked out and unreachable. The drain loop is pointless either way; at most, correct the misleading comment/log text (83-85, 68, 88) so it stops advertising preemption it never performed.

The fix that actually addresses the defect: track checked-out runtimes per Pool (e.g. a mutex-guarded map[*goja.Runtime]struct{} populated in Pool.Get and cleared in Pool.Put), then have DeletePluginPool call Interrupt("plugin unloaded") on the LIVE set. That is the only thing that can unwedge a running loop on unload.

For the per-call deadline, do NOT wrap all of Manager.Run in a blanket time.AfterFunc watchdog (see regression_risk). Prefer either (a) an opt-in, generous per-hook budget sourced from plugin manifest/permissions, defaulting to off or to a value well above legitimate network/chromedp latency, or (b) a watchdog whose ordering is airtight: timer.Stop() must happen-before Put, and Put must ClearInterrupt() unconditionally, with the runtime DISCARDED rather than pooled if the interrupt actually fired (its JS state is now indeterminate mid-execution). Also fix the false advertising in handlePromiseResult (448) and ui.go:107, whose comments claim stopping semantics they do not have.
- **Regression risk:** 1. Blanket watchdog kills legitimately slow hooks. Plugin hook runtimes are bound with synchronous-looking blocking APIs: goja_bindings.BindFetch (goja_plugin.go:178), gojautil.BindAwait (260), plugin.GlobalAppContext.BindAnilist/BindDatabase/BindSystem (266-288), and chromedp bindings whose own timeouts are caller-configurable (chromedp.go:1136,1181,1222 use opts.Timeout seconds). A fixed deadline (e.g. the 30s the finder suggests) would convert slow-but-correct hooks — an Anilist call during rate-limiting, a chromedp scrape, a large debrid fetch — into InterruptedError hook failures. Today those hooks merely run long and then succeed. This is a live-behavior regression on the plugin request path, not a theoretical one.

2. Interrupt landing on a pooled runtime poisons an UNRELATED later hook. Manager.Run does `defer pool.Put(runtime)` (114) and Pool.Put calls ClearInterrupt then sp.Put (212-218). A naive time.AfterFunc that fires in the window after fn returns but after/around Put sets the interrupt flag on a runtime already back in the pool. The next Pool.Get hands that runtime to a different hook, which dies instantly with InterruptedError for no reason — an intermittent, near-unreproducible failure. Any watchdog must Stop() before Put and discard interrupted runtimes rather than recycle them.

3. Interrupt-on-unload racing pool re-creation. external.go:853 fires `go r.gojaRuntimeManager.DeletePluginPool(id)` concurrently, and GetOrCreatePrivatePool (46-57) can re-Set a pool for the same extID. Manager.pluginPools is a result.Map but the Get-then-drain sequence is not atomic, so a reload can have the drain goroutine pull and Interrupt runtimes belonging to the NEW pool. Today that race only discards freshly prewarmed runtimes (self-healing, since Get falls back to factory); with Interrupt, if a live-runtime registry is added, it could interrupt the reloaded plugin's in-flight work. Note extension_playground.go:235,350,442 use `defer DeletePluginPool(ext.ID)` on every playground run — a high-frequency create/delete path that would exercise this race constantly.

4. p.interrupted (goja_plugin.go:64,86,90) is a plain bool read/written from the UI-destroyed goroutine (213-217), ClearInterrupt, and bindHooks (399) with no mutex — pre-existing, but any fix that adds a new goroutine calling into this path widens an existing data race.

#### DATA-DB-1 — CacheLayer's queued-update-sync ticker goroutine is never stopped — leaked on every session rebuild — **open** (medium, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** leak  |  **Slice:** data-user
- **Location:** `internal/platforms/shared_platform/cachelayer_queue.go:38-49 (startQueuedUpdateSync), internal/core/session.go:553-580 (evictSession/UserSession.shutdown)`
- **What:** `shared_platform.NewCacheLayer()` calls `cl.startQueuedUpdateSync()`, which spawns `go func(){ ...; ticker := time.NewTicker(queueSyncInterval); for range ticker.C { ... } }()` with no context, cancel channel, or Close method anywhere on `CacheLayer`. `internal/platforms/anilist_platform/anilist_platform.go` builds a fresh `CacheLayer` on every `NewAnilistPlatform()` call, and `internal/core/session.go`'s `buildUserSession()` (called via `SessionFor` -> `sessions.GetOrSet`) constructs a brand-new `AnilistPlatform`/`CacheLayer` on every session rebuild. `evictSession()` explicitly says it exists so "a relink/logout/token-invalid cycle doesn't leak their listener goroutines," but `UserSession.shutdown()` only tears down `directStream`, `mediaCoord`, `mpvCore`, `videoCore` — it never calls anything on `platformRef`/`CacheLayer`, and `CacheLayer` has no `Close()`/`Stop()` method at all (only `AnilistPlatform.Close()` exists, and it only closes `ap.helper`, not `ap.anilistClient`).
- **Why it matters:** Every login, logout, or automatic token-invalidation event for ANY user (admin or regular) rebuilds a session and therefore a new `CacheLayer`, permanently leaking the old one's ticker + goroutine (each firing every 10s forever, holding references to its `fileCacher`/buckets). On a long-running multi-user server with users relinking/logging out repeatedly (or a flaky token that keeps triggering `checkAndUpdateWorkingState` -> `logoutFunc` -> `evictSession` -> next-request rebuild), this is unbounded goroutine + ticker growth — real memory/goroutine-count degradation over the server's uptime, exactly the leak class documented as the design intent to avoid.
- **Evidence:** cachelayer_queue.go: `func (c *CacheLayer) startQueuedUpdateSync() { go func() { c.syncQueuedUpdates(context.Background()); ticker := time.NewTicker(queueSyncInterval); defer ticker.Stop(); for range ticker.C { c.syncQueuedUpdates(context.Background()) } }() }`. session.go: `func (s *UserSession) shutdown() { ... if s.directStream != nil {...}; if s.mediaCoord != nil {...}; if s.mpvCore != nil {...}; if s.videoCore != nil {...} }` — no platform/CacheLayer teardown, and grep confirms `CacheLayer` defines no `Close`/`Stop` method anywhere in shared_platform.
- **Verifier:** Every quoted line is real and current — I tried to break this claim on four fronts and it survived all of them.

1) The ticker goroutine is verbatim as quoted (cachelayer_queue.go:38-49). `for range ticker.C` has no exit path: the channel is never closed (time.Ticker never closes its channel), and `defer ticker.Stop()` only runs if the loop exits, which it cannot. The goroutine holds `c`, so the CacheLayer + fileCacher + 30-entry buckets map are永 unreachable-but-live.

2) No stop handle exists. The `CacheLayer` struct (cachelayer.go:100-109) has exactly 7 fields — `anilistClientRef, fileCacher, buckets, logger, collectionMediaIDs, lastCollectionUpdate, logoutFunc, pendingUpdateSyncMutex` — no ctx, cancel, or stop channel. A pattern search for `func (c|cl) *CacheLayer) (Close|Stop|Shutdown)` across internal/platforms returns EMPTY. The finder is right that no teardown method exists.

3) Per-session construction confirmed end-to-end: `buildUserSession` (session.go:452-492) unconditionally calls `anilist_platform.NewAnilistPlatform(clientRef, ...)` → `NewCacheLayer` (cachelayer.go:204-255) which ends with `cl.startQueuedUpdateSync()` before returning. `SessionFor` → `a.sessions.GetOrSet(userID, buildUserSession)` means a rebuild after eviction spawns a fresh goroutine.

4) Teardown gap confirmed: `UserSession.shutdown()` (session.go:564-580) touches only directStream/mediaCoord/mpvCore/videoCore — no platform reference at all. `AnilistPlatform.Close()` (anilist_platform.go:64-66) is `{ ap.helper.Close() }` — it never touches `ap.anilistClient`, which IS the CacheLayer. The doc comment on evictSession ("so a relink/logout/token-invalid cycle doesn't leak their listener goroutines") is quoted accurately, making this a genuine gap against stated intent.

Reachability is real but bounded by human action: the only 4 eviction sites are LoginToAnilist / LogoutFromAnilist (admin, anilist.go:214/263) and LoginUserToAnilist / logoutUserFromAnilist (session.go:523/546). There is no periodic or automatic rebuild, so leak rate = login/logout/relink frequency, not a runaway loop.

The finder MISSED a worse consequence than the leak itself: `userAnilistCacheDir` keys only by userID, so a zombie CacheLayer and its live replacement share the same on-disk PendingMediaListUpdatesBucket while each holds its OWN `pendingUpdateSyncMutex` (per-instance, not global). Two ticker goroutines can therefore read the same queued update and both POST it — `deleteQueuedUpdateIfCurrent`'s `sameQueuedUpdate` guard is a check-then-act with no shared lock. Worse, the zombie retains the OLD token; if that token was revoked, its sync hits "user not found" → `go c.logoutFunc()` (cachelayer.go:344-348) → `logoutUserFromAnilist(userID)` → `UpsertAccountForUser(userID, "", "", nil)`, which WIPES the user's freshly-relinked token and evicts the live session. That requires a non-empty queue plus a revoked old token, so it is narrow — but it is a correctness bug, not just memory growth.

Note two paths that do NOT leak: `NewCacheLayer` early-returns the raw client (no goroutine) if `filecache.NewCacher` fails, and `anonymousSession` is built under `anonSessionOnce` so it leaks exactly one CacheLayer for process lifetime — bounded and harmless.
- **Fix:** The finder's fix is directionally right but its exact prescription — "call `platformRef.Get().Close()` from `UserSession.shutdown()`" — would introduce a nil-deref panic and, if written with the accessor instead of the field, a server-wide outage. Corrected:

1. Add `stop chan struct{}` + `stopOnce sync.Once` to `CacheLayer`; set it in `NewCacheLayer` and change the loop to `for { select { case <-c.stop: return; case <-ticker.C: c.syncQueuedUpdates(...) } }`. Add `func (c *CacheLayer) Close() { c.stopOnce.Do(func(){ close(c.stop) }) }` (stopOnce because eviction can race a second eviction of the same userID).

2. `NewCacheLayer` returns the `anilist.AnilistClient` interface and may return the RAW client on `filecache.NewCacher` failure, so `AnilistPlatform.Close()` must type-assert rather than assume: `if c, ok := ap.anilistClient.(interface{ Close() }); ok { c.Close() }` alongside the existing `ap.helper.Close()`.

3. In `UserSession.shutdown()`, use the RAW field with a nil guard — `if s.platformRef != nil { if p := s.platformRef.Get(); p != nil { p.Close() } }` — and NEVER `s.PlatformRef()`, which returns the App-global platform for admin sessions (see regression_risk).

4. Guard the `buildUserSession` → `return a.adminSession()` early-return, which caches a nil-platformRef session into `a.sessions`.

5. Ensure `a.anonSession` is never routed through shutdown().

Separately (worth its own finding): make the queued-update sync mutex process-global or key it by cache dir, since `userAnilistCacheDir` is keyed only by userID and two CacheLayers for one user share the queue file with independent mutexes.
- **Regression risk:** Four concrete breakages, two of which the finder's literal fix would cause:

1. SERVER-WIDE ANILIST OUTAGE (if the fix uses the `PlatformRef()` accessor instead of the `platformRef` field). `UserSession.PlatformRef()` (session.go:586-591) returns `s.app.AnilistPlatformRef` — the App-GLOBAL platform — whenever `s.IsAdmin`. `LoginToAnilist` and `LogoutFromAnilist` both call `evictSession(a.adminUserID())` on every admin login/logout, so `s.PlatformRef().Get().Close()` would close the global platform's helper and CacheLayer, killing AniList for every user on the server on the next admin relink. Must use the raw `s.platformRef` field, which is nil-by-design for admin delegates.

2. NIL-DEREF PANIC on eviction. `adminSession()` (session.go:417-419) returns `&UserSession{app, IsAdmin, UserID}` with `platformRef` NIL, and `buildUserSession` early-returns `a.adminSession()` when `GetUserByID` fails — that value is then stored into `a.sessions` by `GetOrSet`. A later `evictSession` → `shutdown()` → `platformRef.Get()` panics. Needs the nil guard.

3. CUSTOM-SOURCE TEARDOWN on every relink. Routing through `AnilistPlatform.Close()` also fires `ap.helper.Close()` on the per-session `PlatformHelper` (built from the SHARED `extensionBankRef`). Close() currently is not called on the eviction path at all, so this newly runs `helper.Close()` on every login/logout/token-invalid cycle. If the helper's `customsource.Manager` (reachable via `GetCustomSourceManager()`) is still referenced by cached collections or in-flight requests handed out before eviction, custom-source reads for that user could break after a relink. Verify helper.Close() is idempotent and that no live caller holds the manager.

4. ANONYMOUS-TRAFFIC REGRESSION. `a.anonSession` is built once under `anonSessionOnce` and shared process-wide; it is never rebuilt. If any eviction path ever reached it, closing its CacheLayer would permanently and silently disable queued-update sync for all anonymous traffic with no way to respawn it.

Also: stopping the ticker means any updates sitting in the evicted layer's queue stop being retried by that instance. That is fine ONLY because the queue is persisted to disk under a userID-keyed dir and the rebuilt session's CacheLayer picks the same bucket up — worth asserting in a test, since it is the property that makes Close() safe rather than data-losing.

#### NAKA-NAK-3 — Watch-party participant fields still race between two different locks (narrower survivor of F12) — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** nakama
- **Location:** `internal/nakama/watch_party_host.go:618-655 (handleWatchPartyPeerStatusEvent), :658-684 (handleWatchPartyBufferUpdateEvent) vs :699-730 (checkAndManageBuffering), :778-843 (waitForPeersReady)`
- **What:** F12's original 'session.Participants iterated fully unlocked -> fatal concurrent map crash' is FIXED: all map structural writes (insert at :528, deletes at :571/:605) and the checkAndManageBuffering/waitForPeersReady iterations (:718, :834) now consistently hold session.mu, and the map-read call sites in handleWatchPartyPeerStatusEvent/handleWatchPartyBufferUpdateEvent hold wpm.mu (which every map-writer also holds), so the map itself can't crash. However, a narrower field-level race survives: handleWatchPartyPeerStatusEvent (:631-637) and handleWatchPartyBufferUpdateEvent (:671-675) mutate `participant.PlaybackStatus/IsBuffering/BufferHealth/IsReady/LastSeen` while holding only `wpm.mu`, but checkAndManageBuffering (:719-728) and waitForPeersReady read those same fields while holding only `session.mu` - two different mutexes protecting the same WatchPartySessionParticipant struct fields (which have no field-level lock of their own, per the struct at watch_party.go:161-177) provide no mutual exclusion between each other.
- **Why it matters:** Concurrent unsynchronized read/write of participant.IsBuffering/BufferHealth/IsReady between a status-update goroutine and the buffering-management goroutine is a genuine data race (-race detector would flag it): it can yield a stale/torn read of buffering state, causing checkAndManageBuffering to pause/resume playback on inconsistent peer data. Not process-fatal (no map involved), but a real correctness/race bug in the same subsystem F12 was reported against.
- **Evidence:** handleWatchPartyPeerStatusEvent: `wpm.mu.Lock() ... if participant, exists := session.Participants[payload.PeerId]; exists { participant.IsBuffering = payload.IsBuffering; participant.BufferHealth = payload.BufferHealth ... } wpm.mu.Unlock()` (no session.mu taken) vs checkAndManageBuffering: `session.mu.RLock(); for _, participant := range session.Participants { ... if participant.IsBuffering { bufferingPeers++ } } session.mu.RUnlock()` (no wpm.mu taken) - same fields, disjoint locks.
- **Verifier:** Quoted code is real and current. Writers: handleWatchPartyPeerStatusEvent (watch_party_host.go:623-647) and handleWatchPartyBufferUpdateEvent (:663-684) mutate participant.PlaybackStatus/IsBuffering/BufferHealth/UseDenshiPlayer/LastSeen/IsReady while holding ONLY wpm.mu - neither function touches session.mu. Readers: checkAndManageBuffering (:718-730) and waitForPeersReady (:834-843) read participant.IsReady/IsBuffering while holding ONLY session.mu.RLock - neither takes wpm.mu (checkAndManageBuffering's own comment at :698 explicitly forbids holding wpm.mu when calling it, so it can never be the missing mutual exclusion). Disjoint lock sets on the same struct fields; WatchPartySessionParticipant (watch_party.go:161-177) has no field-level lock. Reachability is proven by construction, not inferred: both handlers spawn the reader with `go wpm.checkAndManageBuffering()` at :651/:688 explicitly AFTER releasing wpm.mu, and the reader then blocks on player.PullStatus()/Pause()/Resume(), so it outlives the handler and overlaps the next peer status/buffer event that writes the same fields. waitForPeersReady is an independent 500ms ticker goroutine (spawned :294, :988) reading IsReady with no relationship to wpm.mu at all. This fires in normal steady-state operation with >=1 peer. The finder also correctly scoped it: F12's fatal-map remnant is gone from THESE paths - inserts (:528) and deletes (:571/:605) all hold session.mu, and both iterations hold session.mu.RLock, so the map cannot crash here. Two finder overstatements: (1) "torn read" is wrong - bool/float64 do not tear on amd64/arm64; the real effect is a stale read; (2) impact is self-limiting because the next status tick re-invokes checkAndManageBuffering and re-derives state, so the worst case is a transient wrong pause/resume, not persistent corruption. Separately (out of NAK-3's scope but worth flagging): sendSessionStateToClient (:500-508) JSON-marshals the entire session INCLUDING the Participants map with no lock at all, and is called from the same handlers at :654/:694 concurrently with the session.mu-guarded deletes at :571/:605 - that unlocked map read vs map write is the actual surviving fatal remnant of F12 and is more severe than NAK-3.
- **Fix:** Do NOT add a per-participant mutex (struct is JSON-marshaled; a sync.Mutex makes value copies a vet error) and do NOT make checkAndManageBuffering acquire wpm.mu (violates its :698 contract and inverts waitForPeersReady's bufferMu->session.mu order = deadlock). Instead make session.mu the single owner of participant field access, matching the wpm.mu->session.mu order already used at :516/:526 and :559/:570: in handleWatchPartyPeerStatusEvent (:631-646) and handleWatchPartyBufferUpdateEvent (:671-683), wrap the map lookup + field mutations in session.mu.Lock()/Unlock() while still inside the existing wpm.mu critical section. Readers at :718-730 and :834-843 already hold session.mu.RLock and need no change. While in this code, fix the strictly worse adjacent bug that the finder missed: sendSessionStateToClient (:500-508) marshals session.Participants with no lock while deletes at :571/:605 mutate the map - snapshot the session (or copy the participant slice) under session.mu.RLock before handing it to SendEvent.
- **Regression risk:** The proposed fix "give WatchPartySessionParticipant its own field mutex" is the dangerous option: the struct is JSON-marshaled onto the wire (watch_party.go:161-177, sent via sendSessionStateToClient/broadcastSessionStateToPeers) and adding a sync.Mutex makes any value copy of it a go vet failure; every construction site (e.g. the composite literal at :528) and any peer-side deserialization path would need auditing. The safer variant - have the handlers take session.mu inside wpm.mu - matches the lock order already established at :516/:526, :559/:570, :871/:881 (wpm.mu -> session.mu) and is low risk. The variant that WILL break things is the mirror-image fix of making checkAndManageBuffering acquire wpm.mu: it would violate the explicit contract in its own doc comment at :698 ("should NOT be called while holding wpm.mu as it may need to acquire bufferMu") and introduce a wpm.mu -> session.mu -> bufferMu order that inverts waitForPeersReady's existing bufferMu (:812) -> session.mu (:834) order, creating a real lock cycle between the buffering manager and the peers-ready ticker - i.e. trading a benign stale bool for a deadlock that freezes host playback control. Concretely at risk on the shared path: the buffering auto-pause/auto-resume behavior that currently works (:743-774) and the resume-when-peers-ready handoff (:846-857).

#### PLAY-MC-2 — mediacore.Coordinator.Close() leaks the SetupSharedEffects goroutine on every per-user session eviction — **open** (low, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** leak  |  **Slice:** playback-plane
- **Location:** `internal/mediacore/mediacore.go:110-124 (Close) vs mediacore.go:463-467 (SetupSharedEffects)`
- **What:** SetupSharedEffects() subscribes an internal handler under id "coordinator:effects" and starts `go func() { for event := range sub.Events() {...} }()` (mediacore.go:465-467). Coordinator.Close() only closes `stopCh` and calls `b.Close()` on each backend (mediacore.go:110-124) — it never calls `c.Unsubscribe("coordinator:effects")`. Since `sub.eventCh` is only closed by Unsubscribe/re-Subscribe, and no more events are ever produced once `stopCh` stops the dispatch loop, this goroutine blocks on the empty channel forever.
- **Why it matters:** internal/core/session.go's `shutdown()` (called on every session eviction) explicitly documents this exact leak class ("each spawns a listener goroutine bound to a subscriber channel that is never closed on eviction otherwise") and fixes it for mpvcore (mpvcore.go:136-141, which generically closes ALL entries in `mc.subscribers`) — but mediacore.Coordinator.Close() was never given the equivalent generic-close treatment, so its own internal effects subscriber leaks one goroutine per session teardown. In a multi-user server (the in-progress profile/multi-user feature) with repeated login/logout or session-idle eviction, this accumulates indefinitely.
- **Evidence:** mediacore.go Close(): `close(c.stopCh); for _, b := range c.backends { _ = b.Close() }; return nil` — no `c.subscribers.Range(...)` closing pass, unlike mpvcore.go Shutdown(): `mc.subscribers.Range(func(id string, sub *Subscriber) bool { sub.closed.Store(true); sub.closeOnce.Do(func() { close(sub.eventCh) }); return true })`.
- **Verifier:** Verified against current source, not the finder's paraphrase. Coordinator.Close() (internal/mediacore/mediacore.go:110-124) closes stopCh and calls b.Close() on each backend — there is no subscriber-closing pass. SetupSharedEffects() (mediacore.go:462-467) does `sub := c.Subscribe("coordinator:effects")` then `go func(){ for event := range sub.Events() {...} }()`. The only closers of sub.eventCh are Unsubscribe (mediacore.go:142-147: `Pop` + `closeOnce.Do(close)`) and Subscribe's re-subscribe branch (:134-137); effectsOnce guarantees SetupSharedEffects runs at most once per Coordinator so the re-subscribe branch never fires, and Close() never calls Unsubscribe. A `range` over a never-closed channel blocks forever => the goroutine leaks, retaining the Coordinator and its object graph.

Reachability proven, not assumed: session.go:563 shutdown() calls `_ = s.mediaCoord.Close()` (:570-572), and shutdown() is called from App.evictSession (session.go:552-554), reached from anilist.go:214 (admin links AniList), anilist.go:263 (admin unlinks), session.go:523 (user authenticates), and session.go:546 (logoutUserFromAnilist on invalid token). Each per-user session builds its own Coordinator (session.go:166) and calls SetupSharedEffects (session.go:179), so every eviction leaks one effects goroutine.

The ownership contract seals it: directstream.Manager.Shutdown() (manager.go:405-407) explicitly calls `Unsubscribe("directstream")`, and RegisterEventCallback's goroutine (mediacore.go:154-161) does `defer cancel()` -> Unsubscribe. Every subscriber is unsubscribed by its owner — but "coordinator:effects" is created by the Coordinator itself and has no external owner, so nothing unsubscribes it. mpvcore.go:129-140 is a real precedent, with a comment naming this exact leak class ("every evicted per-session MpvCore (relink/logout) leaks its listener goroutine forever") and the generic Range-close the finder quotes.

Where the finder overreached: it claims "session-idle eviction" and accumulation "indefinitely". There is NO idle/timeout eviction — evictSession's only callers are human auth events (link/unlink/re-auth/token-invalid), and the token-invalid path is self-limiting because logoutUserFromAnilist clears the token via UpsertAccountForUser so the rebuilt session is simulated and won't re-trigger. Leak rate is a handful of goroutines over a process lifetime, not unbounded. Real defect, inflated impact.
- **Fix:** Do NOT add the generic close-all-subscribers loop the finder proposes — it closes channels that dispatch() may be concurrently sending on (see regression_risk). Instead make the effects goroutine itself honor stopCh, which terminates it with NO channel close at all and therefore carries zero send-on-closed-channel risk:

func (c *Coordinator) SetupSharedEffects() {
    c.effectsOnce.Do(func() {
        sub := c.Subscribe("coordinator:effects")
        go func() {
            for {
                select {
                case <-c.stopCh:
                    return
                case event, ok := <-sub.Events():
                    if !ok {
                        return
                    }
                    // ... existing switch unchanged ...
                }
            }
        }()
    })
}

This reuses the stopCh that Close() already closes (mediacore.go:115), so Close() needs no change and the existing Unsubscribe/dispatch concurrency contract is untouched. The `ok` check additionally keeps the goroutine correct if a future caller does Unsubscribe("coordinator:effects").

If the generic Range-close is still wanted for parity with mpvcore.Shutdown, it must first be made safe by serializing dispatch against subscriber teardown (e.g. hold a RWMutex read lock across dispatch's send and a write lock in Close/Unsubscribe) — otherwise it converts a benign, rare, bounded goroutine leak into a potential process crash, which is a strictly worse trade.
- **Regression risk:** The finder's proposed fix (generic close-all-subscribers loop in Close()) risks a send-on-closed-channel panic, which is an uncatchable whole-process crash. dispatch() (mediacore.go:348-366) has a TOCTOU: it checks `sub.closed.Load()` at :350 and only then sends at :355 (critical branch) or :361 (non-critical). Close() only stops the dispatch loop's *next* select iteration via close(stopCh) (:100-101) — it does not wait for an in-flight dispatch() to finish, and the listenToBackendEvents goroutines (:369-461) keep feeding c.eventBus. Concrete failure: a user unlinks AniList mid-playback -> evictSession -> Close() Range-closes eventCh while the dispatch goroutine has already passed the closed.Load() check for that subscriber -> panic: send on closed channel -> server crash. The critical-event branch at :354-358 blocks up to 1 second inside the send select, widening this window to ~1s for critical events rather than a few instructions.

This race class pre-exists via Unsubscribe (directstream.Shutdown -> Unsubscribe races with dispatch identically) and mpvcore.Shutdown has the same shape, so the fix does not invent the hazard — but it widens it by closing EVERY subscriber at once on a path that runs during active playback teardown.

Non-risks I checked and cleared: double-close is safe (closeOnce + closed guard), and the "directstream" subscriber is already Pop'd from the map before Close() runs because session.shutdown() calls directStream.Shutdown() before mediaCoord.Close() (an ordering the comment at session.go:562 explicitly documents), so Close()'s Range would not even see it. RegisterEventCallback consumers exit cleanly on channel close via `defer cancel()`.

#### CORE-EVT-1 — Unlocked read of m.Conns/m.hasHadConnection races with every AddConn/RemoveConn — confirmed live in every Denshi session — **open** (low, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** core-events
- **Location:** `internal/events/websocket.go:132-165 (ExitIfNoConnsAsDesktopSidecar) vs 167-184 (AddConn) / 294-303 (RemoveConn)`
- **What:** ExitIfNoConnsAsDesktopSidecar's 5s ticker loop reads `len(m.Conns)` and `m.hasHadConnection` with no lock: `if len(m.Conns) == 0 && m.hasHadConnection { ... }`. Every other accessor in the same file (AddConn, RemoveConn, SendEvent, GetClientIds, etc.) takes `m.mu` first — AddConn's own comment even says 'Guard m.Conns/m.hasHadConnection like every other accessor' — but this monitor goroutine was left out of that guard. It is not a dormant code path: seanime-denshi/src/main/index.ts:735 always launches the server sidecar with `-desktop-sidecar true`, so this goroutine runs continuously on every Denshi session, racing on m.Conns on every websocket connect/reconnect/disconnect.
- **Why it matters:** A concurrent unsynchronized read of a slice header being mutated by `append` (which can reallocate/resize the backing array) is undefined behavior under the Go memory model — the race detector flags it, and in the worst case it can observe a torn/incorrect length. Since this value directly feeds an `os.Exit(1)` decision (auto-exiting the sidecar when it thinks there are no connections), a racy read can cause the Denshi-embedded server to exit unexpectedly while a client is actually still connected, or fail to exit when it should.
- **Evidence:** `for range ticker.C { if len(m.Conns) == 0 && m.hasHadConnection { ... time.Since(connectionLostTime) > exitTimeout { ... os.Exit(1) } } }` (no m.mu.Lock anywhere in this function) vs. AddConn: `m.mu.Lock(); defer m.mu.Unlock(); m.hasHadConnection = true; m.Conns = append(m.Conns, ...)`. seanime-denshi/src/main/index.ts:735: `args.push("-desktop-sidecar", "true")`.
- **Verifier:** The defect is real and reachable, but the finder's impact story is wrong. VERIFIED REAL: internal/events/websocket.go:145-147 reads `len(m.Conns)` and `m.hasHadConnection` with zero synchronization inside the ticker loop, while AddConn (lines 176-179: `m.mu.Lock(); defer m.mu.Unlock(); m.hasHadConnection = true; m.Conns = append(...)`) and RemoveConn (295-299, same lock) mutate both under m.mu. Every other accessor in the file (SendEvent 336-339, SendEventTo 383-390, GetClientIds 425-426, SendEventToUser 227-235, GetClientPlatform 440-441) takes m.mu. The quoted code is accurate, not paraphrased. VERIFIED REACHABLE (this kills the usual 'dead path' defence): internal/core/app.go:306-307 `if configOpts.Flags.IsDesktopSidecar { wsEventManager.ExitIfNoConnsAsDesktopSidecar() }`; internal/core/flags.go:57 binds that flag to `-desktop-sidecar`; seanime-denshi/src/main/index.ts:735 `args.push("-desktop-sidecar", "true")` sits OUTSIDE the `if (_development)` block (lines 724-733), so it is unconditional for every spawned sidecar. Conns are mutated from per-connection websocket upgrade goroutines, so the concurrency is genuine, not single-goroutine-confined. WHERE THE FINDER IS WRONG (impact inflated): the claimed 'torn/incorrect length' cannot occur — `len(slice)` reads one aligned machine word, so a racing reader observes either the pre-append or the post-append length, never garbage; the torn-read argument would only apply if ptr+len were read together and correlated, which this code does not do. The claimed 'sidecar exits while a client is still connected' is further blocked by the exit gate itself: `time.Since(connectionLostTime) > exitTimeout` with exitTimeout=10s against a 5*time.Second ticker (lines 138/143/155) requires the ==0 observation to survive at least three ticks; a stale read is corrected by the next tick's fresh load, since the `range ticker.C` channel receive is a synchronization operation the compiler cannot hoist the load past. Worst realistic outcome is the exit decision moving by one 5s tick in a process that is already intentionally exiting. So: a genuine Go memory-model violation and race-detector hit, trivially fixed, with benign runtime consequences.
- **Fix:** Read the two fields through a small locked helper and keep the lock OUT of the loop-scoped defer chain. Add: `func (m *WSEventManager) connState() (n int, hadConn bool) { m.mu.Lock(); defer m.mu.Unlock(); return len(m.Conns), m.hasHadConnection }` and change line 147 to `if n, had := m.connState(); n == 0 && had {`. This keeps the lock hold to two field reads, matches the snapshot-then-release discipline the rest of the file already uses, and does not touch the exit timing semantics. An atomic counter would also work but is worse here: it would duplicate state that m.Conns already carries and could drift from the slice. Given the corrected severity, this is a cleanliness/race-detector fix (it lets `go test -race` run clean on this package), not an urgent correctness patch — the exit gate's 3-tick/10s hysteresis already makes the observable behaviour correct.
- **Regression risk:** The danger is entirely in HOW the lock is added, not in locking per se. m.mu is the single hot mutex guarding all websocket fan-out (SendEvent, SendEventToLoggedIn, SendEventTo, SendStringTo, SendEventToUser, SendEventToUserOrUnscoped, SendEventToIfOwner, GetClientIds, GetClientPlatform, SetConnUserID, AddConn, RemoveConn). Concrete regression: the idiomatic-looking `m.mu.Lock(); defer m.mu.Unlock()` placed at the top of the goroutine's func literal (matching the style of every other accessor in the file) would hold m.mu for the entire lifetime of the ticker loop, deadlocking every websocket connect, disconnect and event send process-wide — turning a benign race into a total ws outage on every Denshi session. Equally, `defer m.mu.Unlock()` written INSIDE the `for range ticker.C` body never runs until the loop ends, producing the same permanent hold. Correct placement is a lock/unlock pair scoped to just the two field reads. Second, smaller risk: holding m.mu across the blocking work would re-introduce the F7 problem the file explicitly documents at lines 99-101 and 334-335 ('holding m.mu across the blocking WriteJSON lets one slow/hung client stall all ws traffic process-wide'). A correctly-scoped snapshot read is contention-free — the lock is taken once per 5s and every other holder only snapshots and releases before writing.

#### LIBR-SYNC-2 — shouldUpdateLocalCollections bool written without the lock that guards its read — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** library-local
- **Location:** `internal/local/sync.go:106, :131, :442 (unlocked writes) vs :144-166 checkAndUpdateLocalCollections (locked read/reset under q.mu)`
- **What:** `q.shouldUpdateLocalCollections = true` is set directly with no lock in processAnimeJobs (:106), processMangaJobs (:131), and refreshCollections (:442), while checkAndUpdateLocalCollections reads and later resets the same field entirely under `q.mu.Lock()`.
- **Why it matters:** An unlocked write racing the locked read/reset can be lost or reordered relative to the queue-length check, so the 'sync finished, tell the frontend' path (`synchronizeCollections` + `events.SyncLocalFinished`) can be skipped after a real completion, or fire based on a stale flag — leaving the client's sync-queue UI stuck or collections not refreshed after a completed sync.
- **Evidence:** q.shouldUpdateLocalCollections = true // sync.go:106, no lock held
...
func (q *Syncer) checkAndUpdateLocalCollections() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shouldUpdateLocalCollections { ... }
}
- **Verifier:** The quoted code is real and current. sync.go:106 (processAnimeJobs) and :131 (processMangaJobs) write `q.shouldUpdateLocalCollections = true` with no lock held — the preceding critical sections use `q.queueStateMu`, a DIFFERENT mutex, and are unlocked before the write. :442 (refreshCollections) does the same. checkAndUpdateLocalCollections (:144-166) reads at :149 and resets at :159 entirely under `q.mu.Lock()`. Concurrency is reachable and live, not theoretical: NewQueue (called at manager.go:175) spawns processAnimeJobs and processMangaJobs as two separate goroutines (sync.go:87-88), each of which both writes the flag unlocked and calls the locked reader; refreshCollections adds a third concurrent writer on HTTP request goroutines via UntrackAnime (manager.go:395) and UntrackManga (manager.go:451). An unlocked write concurrent with a locked read, with no happens-before between the goroutines, is a textbook Go data race that `go test -race` would report. HOWEVER, the finder's impact reasoning is wrong and caps severity: (1) every unlocked write stores the same value (`true`), so there is no write-write conflict among the unlocked writers — only unlocked-true vs locked-false — and a bool is a single-byte store on amd64/arm64, so it cannot tear; (2) the claimed failure (flag lost -> SyncLocalFinished skipped) is NOT caused by the missing lock and would survive the proposed fix. The lost update comes from a logic gap: :106 sets the flag BEFORE the slow synchronizeAnime call, and the emptiness test at :151 (`len(q.animeJobQueue) == 0`) does not count the job already popped off the channel by `range`, so a sibling goroutine's checkAndUpdateLocalCollections can consume and reset the flag while the anime job is still mid-sync. Holding q.mu around the write makes that interleaving atomic but not ordered — the identical lost wakeup still occurs. So: real memory-model violation, correct location, wrong mechanism, and a fix that does not fix what it claims to.
- **Fix:** Two separable issues; do not conflate them. (1) For the actual data race, use `atomic.Bool` for shouldUpdateLocalCollections rather than the finder's first suggestion of taking q.mu around the write. atomic.Bool is not merely a stylistic alternative here — wrapping the :442 write in q.mu deadlocks, because refreshCollections calls checkAndUpdateLocalCollections at :443 which acquires q.mu, and sync.RWMutex is non-reentrant. Note the check-then-reset at :149/:159 must then become CompareAndSwap(true, false) to stay atomic against a concurrent set. (2) For the real user-visible symptom the finder described (sync-finished skipped / collections stale after a completed sync), the fix is unrelated to synchronization primitives: track in-flight jobs, not just channel depth. The emptiness test at :151 only checks `len(q.animeJobQueue) == 0 && len(q.mangaJobQueue) == 0`, which is already false-negative for a job that `range` has popped but not finished. Add an in-flight counter (or sync.WaitGroup) incremented before synchronizeAnime/synchronizeManga and decremented after, and require it to be zero alongside the queue-length check. Without (2), applying (1) alone silences the race detector and changes no observable behavior.
- **Regression risk:** Concrete and already evidenced in-tree: taking q.mu around the :442 write in refreshCollections is an immediate self-deadlock, because :443 calls checkAndUpdateLocalCollections which does q.mu.Lock() and Go's sync.RWMutex is not reentrant. The codebase already carries the scar of this exact lock-ordering hazard — runDiffs holds q.mu (:456-457) and its call to q.refreshCollections() at :526 is commented out, which is precisely the deadlock that path would hit. Any reviewer applying the finder's stated fix ("write only while holding q.mu") verbatim to all three sites breaks UntrackAnime/UntrackManga (manager.go:395, :451) — currently-working request paths — by hanging the request goroutine while holding q.mu, which then wedges both processAnimeJobs and processMangaJobs at :114/:139 and freezes local sync process-wide. The atomic.Bool variant avoids this entirely. Second, subtler risk: if the check-then-reset at :149/:159 is converted to atomics without CompareAndSwap, the reset can clobber a set that arrived between the read and the write, making the lost-wakeup marginally more likely than it is today. Third, the in-flight-counter fix (2) makes checkAndUpdateLocalCollections fire strictly less often; since it is the sole emitter of events.SyncLocalFinished (:158) and a writer to doneUpdatingLocalCollections (:161), any bug that leaves the counter non-zero (early return, panic between increment and decrement — note synchronizeCollections already relies on util.HandlePanicInModuleWithError at :184) would permanently stall the frontend sync UI instead of merely refreshing it late. Guard the decrement with defer.

#### DATA-DB-3 — Unsynchronized package-level `accountCache` read/written across concurrent goroutines — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** data-user
- **Location:** `internal/database/db/account.go:10, 24-26, 41-51, 62, 94-124`
- **What:** `var accountCache *models.Account` is a bare package-level variable with no mutex. It is written from `UpsertAccount` (line 24/26), `UpsertAccountForUser` (line 120), and read/written from `GetAccount` (lines 41-49, 62) — all exported `*Database` methods that are called concurrently from different HTTP request goroutines (login, logout, any code path resolving `GetAnilistToken()`).
- **Why it matters:** This is a genuine data race (flagged by `go test -race`): concurrent calls to `LoginToAnilist`/`LogoutFromAnilist` (which call `UpsertAccount`) racing against any concurrent `GetAccount()`/`GetAnilistToken()` call (used by request handlers, plugin DB bridge, UI context) read/write the same pointer without synchronization — undefined behavior under Go's memory model even though pointer writes rarely tear in practice.
- **Evidence:** `var accountCache *models.Account` then `func (db *Database) UpsertAccount(...) { ... if acc.Username != "" { accountCache = acc } else { accountCache = nil } ... }` and `func (db *Database) GetAccount() (*models.Account, error) { if accountCache != nil { return accountCache, nil } ... accountCache = &acc ... }` — no mutex guards any of these accesses.
- **Verifier:** CONFIRMED, but the finder undersold the proof and oversold the severity. The quoted code is verbatim real and current: internal/database/db/account.go:10 is a bare `var accountCache *models.Account`; grep over the whole repo shows the ONLY references are that decl plus writes at 24/26 (UpsertAccount), 48 and 62 (GetAccount), 120 (UpsertAccountForUser), and a read at 41-42 — no mutex, no atomic, no sync.Once, no accessor funnel. Concurrent reachability is real and stronger than claimed: it needs no login/logout at all, because GetAccount itself WRITES the cache on a miss (lines 48, 62), so two concurrent HTTP requests on a cold cache race writer-vs-writer. Reader and writer goroutines are independent by construction: HTTP handlers reach UpsertAccount via App.LoginToAnilist (internal/core/anilist.go:198) and App.LogoutFromAnilist (anilist.go:251), while GetAccount/GetAnilistToken are called from plugin goja VM goroutines (internal/plugin/database.go:199, 208, 222), the plugin UI context (internal/plugin/ui/context.go:193), App.UseOfficialAnilistClient (anilist.go:70) and InitOrRefreshAnilistData (modules.go:1029). The only existing guard I found — `a.logoutInProgress.CompareAndSwap` at anilist.go:237 — serializes logout-vs-logout ONLY and does nothing for readers. So `go test -race` would flag it. Severity is low, not medium: on amd64/arm64 (the only targets shipped) a word-sized pointer store cannot tear, so the worst real outcome is a stale/nil read. The one genuine functional consequence, which the finder missed, is a stale-token resurrect: GetAccount reads the DB row while still logged in, LogoutFromAnilist then writes the empty row and nils the cache, then GetAccount executes `accountCache = &acc` at line 62 and re-installs the logged-out account. That self-heals — logout only fires when the token is already invalid, so the next request 401s and re-triggers LogoutFromAnilist (CAS released by then), re-nilling the cache. Transient, narrow interleave, no auth bypass, no data loss.
- **Fix:** Keep `accountCache` package-level (do NOT move it into the `Database` struct — it is deliberately shared across all `*Database` instances and no test resets it) and guard it with a package-level `sync.RWMutex` behind two tiny helpers, e.g. `func cachedAccount() *models.Account` (RLock) and `func setCachedAccount(a *models.Account)` (Lock). Replace the five raw accesses (lines 24, 26, 41-42, 48, 62, 120) with those helpers. Critically, do NOT hold the lock across the DB calls inside `GetAccount` (`db.GetAdminUser()` at :46, `db.GetAccountByID()` at :47, `db.gormdb.Last()` at :54) — take RLock only for the line 41-42 read, release, do the queries unlocked, then take Lock only for the line 48/62 store. Holding a write lock for the whole function would serialize every `GetAnilistToken()` caller (plugin VM goroutines call it per-request) behind a SQL round-trip on cold cache. Accept the benign duplicate-query race on cache miss; last writer wins and both write an equivalent row.
- **Regression risk:** Three concrete risks on this shared path. (1) Latency/serialization: GetAccount is on hot paths — GetAnilistToken() is invoked per plugin call from goja VM goroutines (plugin/database.go:199), from the plugin UI context (ui/context.go:193), and from applyRuntimeAnilistClient (anilist.go:110). If the fix wraps the whole GetAccount body in a single write lock (the obvious naive patch), every one of those callers serializes behind the cold-cache SQL round-trips at lines 46-47/54, converting a lock-free read into a global chokepoint on plugin execution. (2) Deadlock/self-lock: GetAccount calls db.GetAdminUser() (:46) and db.GetAccountByID() (:47) mid-function, and GetAnilistToken (:128) calls GetAccount. If the guard is added at the wrong granularity — e.g. a non-reentrant Lock taken in both GetAnilistToken and GetAccount, or a lock held while calling into another db method that later acquires it — the server hangs on first token read rather than racing. (3) Semantic change if the var is moved into the Database struct as the finder proposes: today the cache is package-level and therefore SHARED across every *Database instance in the process; per-instance caching changes which account a second Database sees, and the same move silently changes test isolation. grep shows no test touches accountCache today, so this risk is latent rather than active — but it is a behavior change disguised as a synchronization fix, which is why the mutex-on-the-existing-var form is the correct minimal patch.

#### DATA-DB-4 — TrimMediastreamVideoFiles wipes the ENTIRE in-memory filecache store map unconditionally, not just when trimming actually happens — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** bug  |  **Slice:** data-user
- **Location:** `internal/util/filecache/filecache.go:465-484 (TrimMediastreamVideoFiles)`
- **What:** The function's own comment says it should "clear all mediastream video file caches if the number of files exceeds the given limit," and correctly gates the on-disk `os.RemoveAll` behind `if len(files) > 10`. But `c.stores = make(map[string]*CacheStore)` sits AFTER that `if` block, unguarded by it, so it executes every single call regardless of whether the video-file count exceeded the limit — nuking every in-memory cache bucket for the whole `Cacher` (AniList data, sessions, everything using this `Cacher` instance), not just mediastream buckets.
- **Why it matters:** `InitOrRefreshMediastreamSettings()` in `internal/core/modules.go:896` calls this on every app start and every time mediastream settings are refreshed. Each call forces every cache bucket to be reloaded from disk on next access even when there was nothing to trim, and matches the previously-documented 'filecache in-memory-cache clear gotcha.' It also opens a narrow correctness race: a goroutine holding a `*CacheStore` pointer obtained just before the wipe can still write through it (`saveToFile()` = `os.Create` + full-map re-encode) concurrently with a goroutine that, after the wipe, calls `getStore()` again and gets a different `*CacheStore` object for the same bucket/file — two independent, unsynchronized writers to the same on-disk file.
- **Evidence:** func (c *Cacher) TrimMediastreamVideoFiles() error { c.mu.Lock(); defer c.mu.Unlock(); files, err := os.ReadDir(filepath.Join(c.dir, "videofiles")); if err != nil { return nil }; if len(files) > 10 { for _, file := range files { _ = os.RemoveAll(...) } }; c.stores = make(map[string]*CacheStore); return err } — the store wipe runs even when len(files) <= 10.
- **Verifier:** The quoted code is real and current. filecache.go:465-484 reads exactly as the finder claims: `if len(files) > 10 { for ... os.RemoveAll(...) }` then `c.stores = make(map[string]*CacheStore)` at line 482, OUTSIDE the `if`, followed by `return err`. The doc comment at 464 says "clears all mediastream video file caches if the number of files exceeds the given limit," so the unguarded wipe genuinely contradicts the stated gating. The caller is real too: internal/core/modules.go:896, in a goroutine inside InitOrRefreshMediastreamSettings. So the literal claim in the title survives.

But the finder's impact reasoning is largely wrong, which is why this is low, not medium:

1. NO DATA LOSS — the cache is write-through. Every mutation (Set:126, Delete:213, Empty:251, Range:156, DeleteIf:239, SetPerm:279, DeletePerm:310, DeletePermOldest:334, EmptyPerm:346) calls store.saveToFile() immediately while holding store.mu. Nothing lives only in memory.

2. THE WIPE IS SEMANTICALLY A NO-OP. This is the decisive point the finder missed. Unlike ClearMediastreamVideoFiles (440-462), TrimMediastreamVideoFiles never calls RemoveAllBy — it deletes only the videofiles/ dir contents, never the .cache files on disk. So after the map wipe, the next getStore(name) (99-115) recreates the store and loadFromFile()s the exact same on-disk content back. Post-wipe in-memory state == pre-wipe state, modulo expiry pruning. The entire cost is one re-read of the .cache files that get touched again — not a correctness change.

3. THE RACE IS NOT CAUSED BY THIS BUG. The finder's "two unsynchronized writers to the same file" window (P1 obtained pre-wipe with its own store.mu, P2 created post-wipe with a different store.mu, both doing saveToFile() = os.Create truncate + full re-encode) is real in principle — but it is inherent to the wipe-the-map design and exists identically in Clear() (91-96), ClearMediastreamVideoFiles() (459), Remove() (259-261) and RemoveAllBy() (409). The proposed fix does not eliminate it; it only makes it rarer. Attributing the race to the missing `if` guard is incorrect.

4. IMPACT IS BOUNDED AND THE FIX IS INCOMPLETE. Trim runs only at module init / mediastream settings refresh, and only on the TranscodeEnabled branch. The else-branch at modules.go:899 calls ClearMediastreamVideoFiles, which wipes the whole map unconditionally by design. So "every InitOrRefreshMediastreamSettings nukes the map" stays true after the finder's fix — patching Trim alone changes nothing about that claim.

Also noted but not the finding: `return err` at 484 is always nil, since the only non-nil ReadDir error already returned at 471-473. Dead but harmless.

Real, provable, matches the doc-comment mismatch — but it is a code-hygiene defect with near-zero runtime consequence, not a medium bug.
- **Fix:** Minimal and sufficient: move `c.stores = make(map[string]*CacheStore)` inside the `if len(files) > 10` block so the code matches its comment, and change the trailing `return err` to `return nil` (err is provably nil there). Prefix-scoping is optional and safe — mediastream buckets are named `mediastream_mediainfo_<hash>` (internal/mediastream/videofile/info.go:144), matching the "mediastream" prefix filter ClearMediastreamVideoFiles already uses at 454-456 — but it buys almost nothing given point 2 above (the wipe is a no-op re-read either way).

Do NOT bundle the race fix into this. If the two-writers-to-one-file window is worth closing, it must be fixed at the design level for every wipe site (Clear, ClearMediastreamVideoFiles, Remove, RemoveAllBy), e.g. by making CacheStore.saveToFile write via temp-file + atomic rename, or by marking evicted stores dead so a stale pointer's saveToFile becomes a no-op. That is a separate, larger finding — file it on its own if desired.
- **Regression risk:** Low but non-zero, and concentrated in the part of the fix the finder over-reached on:

1. Prefix-scoping the wipe (the finder's second half) is the riskier half. The current whole-map wipe is a blunt instrument that guarantees no in-memory store survives a videofiles purge. If any bucket caching videofile-derived state is NOT named with a "mediastream" prefix, scoping would leave it in memory pointing at subs/att dirs that os.RemoveAll just deleted (paths built at videofile/extract.go:15,19 as videofiles/<hash>/subs and /att). I only verified the mediainfo bucket (info.go:144) carries the prefix; I did not enumerate every videofile-derived bucket. Since scoping buys nothing (the wipe re-reads identical data from disk anyway), taking this half on adds risk for no gain.

2. Gating the wipe behind `len(files) > 10` slightly widens the window in which a stale *CacheStore pointer stays live across a settings refresh — the opposite direction from the finder's race concern, though still bounded by the same pre-existing design.

3. Blast radius of the file itself is wide — filecache.Cacher backs AniList/shared_platform cachelayer (cachelayer.go:282,300,556,772,790), manga (repository.go:112), onlinestream (repository.go:111,119), franchise (franchise.go:377,383), continuity/last-watched (manager.go:73,75, the durable `_lw` store) and the anizip artwork bucket (metadata.go:19). Any change to getStore/saveToFile semantics would touch all of them. Confining the edit to the two lines inside TrimMediastreamVideoFiles keeps that radius at zero — one more reason to skip the scoping half.

4. Fork-behavior note: this touches the documented "filecache in-memory-cache clear gotcha" (see the loading-screen TMDB logo fallback memory), where a stale in-memory cache previously masked a disk update. Making the wipe rarer is directionally against whatever workaround that gotcha produced. Worth a glance before merging, though point 2 in my reason (wipe re-reads the same disk content) means Trim was never the mechanism doing that clearing — ClearMediastreamVideoFiles, which calls RemoveAllBy, is.

#### MEDI-NAK-2 — getPageDimensions fires one unbounded goroutine per manga page with no concurrency cap, unlike the download path — **open** (low, finder said high)

- **Verdict:** CONFIRMED  |  **Category:** perf  |  **Slice:** media-sources
- **Location:** `internal/manga/chapter_page_container.go:203-219 (Repository.getPageDimensions)`
- **What:** On a page-dimensions cache miss, this loops over every page of a chapter and spawns a goroutine per page with `go func(page ...) { ... buf, err = manga_providers.GetImageByProxy(page.URL, page.Headers) ... }(page)` and no semaphore/worker-pool bound — every page's full image is downloaded and buffered into memory ([]byte) concurrently. This is called from the normal (non-download) manga-reading path GetMangaPageContainer whenever the page-dimensions cache is empty. Compare to the chapter downloader's downloadChapterImages, which explicitly computes `calculateBatchSize` and gates concurrency through `semaphore := make(chan struct{}, batchSize)` (max 5) for the exact same kind of per-page image fetch.
- **Why it matters:** For chapters with many pages (long-strip/webtoon-style releases can run into the hundreds), this spikes to hundreds of simultaneous full-image HTTP downloads held fully in memory at once with zero concurrency limit — the same unbounded-buffer shape already diagnosed as the manga long-strip OOM on the iOS client, but here on the server, and it can affect every connected client since it's shared read-path code, not download-specific.
- **Evidence:** for _, page := range pages { wg.Add(1); go func(page *hibikemanga.ChapterPage) { defer wg.Done(); ...; buf, err = manga_providers.GetImageByProxy(page.URL, page.Headers) ... }(page) } — no semaphore, unlike downloadChapterImages's `semaphore := make(chan struct{}, batchSize)`.
- **Verifier:** The quoted code is real and current (chapter_page_container.go:203-235, verified verbatim): `for _, page := range pages { wg.Add(1); go func(...){...GetImageByProxy...}(page) }` with only a `sync.Mutex` guarding the map write — no semaphore, no worker pool. The comparison to the downloader is also accurate: chapter_downloader.go:229-252 has `calculateBatchSize` (max 5) + `semaphore := make(chan struct{}, batchSize)` with acquire-before-`wg.Add`. And there is genuinely no throttle downstream — `GetImageByProxy` (providers/proxy_images.go:4) → `ImageProxy.GetImage` (util/proxies/image_proxy.go:16) constructs a **fresh `req.C()` client per call**, so there isn't even a shared connection pool / MaxConnsPerHost to accidentally bound it, and there's no timeout. `io.ReadAll` buffers each full image. So the unbounded-concurrency mechanism is proven and reachable.

BUT the finder's reachability and impact claims are both wrong, and they are what inflate this to "high":

1. NOT the "normal manga-reading path ... whenever the page-dimensions cache is empty". `getPageDimensions` early-returns `nil, nil` at line 184 when `!enabled`. `enabled` is the `doublePage` arg. Of the two callers: download.go:204 passes `false` hardcoded (so the download path never reaches it at all), and handlers/manga.go:459 passes `b.DoublePage`, which the client sets at chapter-reader-drawer.tsx:129 as `doublePage: readingMode === MangaReadingMode.DOUBLE_PAGE`. So it fires only in the opt-in Double Page reading mode, on a dimensions-cache miss, once per chapter (result is persisted at line 237).

2. The stated worst case is self-refuting. The finder justifies "hundreds of simultaneous downloads" via "long-strip/webtoon-style releases". But `MangaReadingMode.LONG_STRIP` is a *distinct* mode from `DOUBLE_PAGE` (manga-chapter-reader.atoms.ts:123-127) and is the **default** (`__manga_readingModeAtom` defaults to `LONG_STRIP`, line 129). Long-strip sends `doublePage: false` → line 184 early-return → this loop never runs. The exact scenario cited as the danger is the one where the code is dead. Double-page mode is for paged manga (typically ~20-50 pages), so realistic burst is tens of concurrent fetches, ~10-50MB transient, not an OOM on an 8GB Pi 5. Real residual risk is provider rate-limiting/429s and a socket burst, not memory exhaustion.

Worth flagging: a *worse* co-located bug the finder missed. Line 215 uses `buf, err = ...` (plain `=`), and there is no local `err` in scope — `buf` is local (`var buf []byte`, line 211) but `err` resolves to the **captured named return** of `getPageDimensions`. N goroutines write that one word concurrently = a genuine data race (`go test -race` would flag it), also read by the `defer util.HandlePanicInModuleWithError(..., &err)` at line 181. Line 220 shadows correctly with `:=`, which is what makes 215 look intentional. It's currently benign only because line 241 returns a literal `nil` and both call sites discard the error with `_`.
- **Fix:** Bounding concurrency is right, but do NOT copy `calculateBatchSize` verbatim as proposed — it computes `numURLs / 10` capped at 5, which yields **batchSize == 1 for any chapter under 10 pages**, fully serializing short chapters and making this worse than the bug. Use a flat cap instead: `sem := make(chan struct{}, 5)`, acquire before `wg.Add(1)`, release in the goroutine's defer (mirroring chapter_downloader.go:244-252). Skip the semaphore entirely when `page.Buf != nil` (local provider — no HTTP, no reason to throttle).

Fix the `err` race in the same pass, since it's a one-word change in the same lines and is the more defensible defect: line 215 → `var fetchErr error; buf, fetchErr = ...; if fetchErr != nil { return }`, so the goroutines stop writing the captured named return.

Optional, and the actually-large win if latency is a concern: `getImageNaturalSizeB` → `util.DetectImageFormatAndDimensions` only needs the image header, but `ImageProxy.GetImage` does `io.ReadAll` of the entire body. A header-range/streaming read would cut both the memory and the wall-clock far more than any semaphore, and would make the concurrency cap nearly free.
- **Regression risk:** The fix directly trades first-open latency for the concurrency bound, on a path where the user is blocked waiting. `GetMangaPageContainer` is called synchronously from the echo handler (handlers/manga.go:459) and `wg.Wait()` (line 235) blocks the HTTP response until every page is fetched. Today all N pages fetch fully in parallel, so wall time ≈ the single slowest page. Capping at 5 turns a 50-page chapter into 10 sequential rounds — roughly a 10x slower first open of a double-page chapter, directly visible as the reader hanging. This is masked in normal use because line 237 caches the result, so only the first open of each chapter pays it — but that first open is exactly the one users notice. If `calculateBatchSize` is copied as the finder proposes, sub-10-page chapters drop to batchSize=1 and serialize completely, which is a strictly worse regression than the bug being fixed.

Blast radius is otherwise tight and argues for the fix being safe: `getPageDimensions` is private with exactly two call sites, both in chapter_page_container.go (lines 101 and 156), both already discarding the error with `_`, and both gated on the same `doublePage` flag. The downloader (download.go:204) passes `false` and never enters the function, so the chapter-download path cannot regress. Nothing outside this file reads the goroutine's behavior, and `PageDimensions` is populated identically either way — only timing changes.

One subtle trap: acquiring the semaphore *before* `wg.Add(1)` in the parent loop (as chapter_downloader.go does) makes the parent goroutine block in the loop body. That's fine here since there's no cancel channel, but it means a single hung `GetImageByProxy` — which has **no timeout** (image_proxy.go:16-35) — would stall the whole loop and the HTTP request indefinitely, where today one hung page only delays `wg.Wait()` while the rest complete. Bounding concurrency without adding a client timeout converts a slow-page annoyance into a wedged request.

#### MEDI-NAK-3 — Data race on the named return `err` inside getPageDimensions' per-page goroutines — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** race  |  **Slice:** media-sources
- **Location:** `internal/manga/chapter_page_container.go:209-219`
- **What:** getPageDimensions has signature `func (r *Repository) getPageDimensions(...) (ret map[int]*PageDimension, err error)` — a named return `err`. Inside the per-page goroutine, `buf, err = manga_providers.GetImageByProxy(page.URL, page.Headers)` assigns to that same outer `err` (using `=`, not `:=`, so it is the captured closure variable, not a new local) from every concurrently running goroutine, with no mutex protecting it.
- **Why it matters:** Concurrent unsynchronized writes to a shared variable from multiple goroutines is undefined behavior under the Go memory model and will be flagged by `go test -race`; it also means the function's own error state is nondeterministically clobbered by whichever page's fetch happens to write last, even though the function currently discards it (`return pageDimensions, nil`) — a latent bug if that return is ever changed to propagate `err`.
- **Evidence:** `var buf []byte; if page.Buf != nil { buf = page.Buf } else { buf, err = manga_providers.GetImageByProxy(page.URL, page.Headers); if err != nil { return } }` — `err` here is the function's named return, written from N concurrent goroutines with no lock (the `mu sync.Mutex` in scope only guards `pageDimensions`, not `err`).
- **Verifier:** Read internal/manga/chapter_page_container.go:181-242. The quoted code is real and current. Signature at :181 is `func (r *Repository) getPageDimensions(enabled bool, provider string, mediaId int, chapterId string, pages []*hibikemanga.ChapterPage) (ret map[int]*PageDimension, err error)` — named return `err`. Inside the per-page goroutine at :209-233, `var buf []byte` is a local but :215 is `buf, err = manga_providers.GetImageByProxy(page.URL, page.Headers)` using `=`, so `err` resolves to the function-scope named return captured by every goroutine. The `mu := sync.Mutex{}` at :205 is only locked at :226-232 around the `pageDimensions` map write — nothing guards `err`. (:220 `width, height, err := getImageNaturalSizeB(buf)` DOES shadow with a fresh local, so only the :215 write / :216 read pair touches the shared variable.)

Concurrent reachability proven, not assumed: `page.Buf` is populated only by the local provider (internal/manga/providers/local.go:440 and :507). For every remote provider (comick, mangapill, etc.) `page.Buf == nil`, so all N goroutines take the else branch and hit :215 simultaneously. Both callers (chapter_page_container.go:102 and :156) pass the full pages slice, and the :99 cache-HIT caller is explicitly gated on `!isLocalProvider`. The only gate is the `enabled` (doublePage) flag. So `go test -race` would flag this on any multi-page remote chapter with double-page mode on.

The finder actually UNDERSOLD the functional impact: :216 also READS the shared `err`, so goroutine B can fetch successfully and then be forced into an early `return` by goroutine A's failure landing between B's assign and B's check — B's page dimension is silently dropped, and the truncated map is then persisted at :237 via `fileCacher.Set`, so the bad result is cached rather than transient.

But the finder OVERSOLD severity and got the fix wrong. Both callers discard the error (`pageDimensions, _ := ...`), :241 hard-returns `nil`, and `defer util.HandlePanicInModuleWithError(..., &err)` at :182 only writes `err` on panic (after wg.Wait()), so the "latent bug if the return is ever propagated" framing is speculative. Observable damage is a missing double-page layout hint on an opt-in feature.
- **Fix:** The finder's proposed fix `buf, err := manga_providers.GetImageByProxy(page.URL, page.Headers)` DOES NOT COMPILE: `:=` inside the else block declares a NEW `buf` shadowing the outer `var buf []byte`, and that new `buf` is never used within the block, so the Go compiler errors with "declared and not used". Even if it compiled, the outer `buf` would remain nil and every remote page would lose its dimensions at :220.

Correct fix — keep `buf` assigned while giving the error its own scope inside the goroutine:

  var buf []byte
  if page.Buf != nil {
      buf = page.Buf
  } else {
      b, ferr := manga_providers.GetImageByProxy(page.URL, page.Headers)
      if ferr != nil {
          return
      }
      buf = b
  }

This removes both the write and the read of the shared named return, eliminating the race and the cross-goroutine early-return bug in one step.
- **Regression risk:** Essentially none, and I traced why. The named return `err` is never observed on the success path: :241 explicitly does `return pageDimensions, nil`, so the value the goroutines clobber is already discarded. Both call sites — chapter_page_container.go:102 (`pageDimensions, _ := r.getPageDimensions(doublePage, provider, mediaId, chapterId, container.Pages)`) and :156 (`pageDimensions, _ := r.getPageDimensions(doublePage, provider, mediaId, chapterId, pages)`) — throw the error away with `_`, so no caller can observe a behavior change. The only other consumer of `&err` is the `util.HandlePanicInModuleWithError` defer at :182, which writes (not reads) `err` and only on panic, after `wg.Wait()` has returned; localizing the fetch error does not touch that path.

The one concrete thing to be careful about is the fix itself rather than the surrounding code: naively using `:=` shadows `buf` (compile error today; silently-nil buffers if someone "fixes" the compile error by restructuring wrong), which would break dimension extraction for ALL remote-provider pages — i.e. break the exact currently-working behavior the change is meant to protect. Use the `b, ferr := ...; buf = b` form above.

Second-order note: correcting the race will make previously-dropped page dimensions start being computed and cached for remote chapters where a sibling page's fetch used to poison the batch. That is the intended repair, but any already-cached truncated PageDimension maps in the `page-dimensions` filecache bucket will persist until evicted, so the improvement won't appear on cached chapters without a cache clear.

#### MEDI-NAK-4 — Local manga provider leaks a file handle per page when loading a chapter (zip entries and directory files never closed) — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** leak  |  **Slice:** media-sources
- **Location:** `internal/manga/providers/local.go:427-433 (zip/cbz case), 494-500 (directory case)`
- **What:** In FindChapterPages, both branches that load chapter pages into memory open a reader per page and never close it: the CBZ/ZIP branch does `page, err := f.Open(); ...; buf, err := io.ReadAll(page)` with no `page.Close()` (nor a defer) anywhere in the loop; the plain-directory branch does the same with `page, err := os.Open(filepath.Join(fullpath, entry.Name()))` — again never closed.
- **Why it matters:** Every call to FindChapterPages (i.e. every chapter opened by a user of the Local manga provider) leaks one open file descriptor per page in that chapter. For directories of images (the common layout for long-strip/webtoon-style local collections, which can have hundreds of page files) or for CBZ archives with many pages, repeated navigation through a library will accumulate open FDs until the process hits its descriptor limit, at which point subsequent os.Open/zip reads start failing across the whole server process, not just manga.
- **Evidence:** case ".zip", ".cbz": ... `page, err := f.Open()` ... `buf, err := io.ReadAll(page)` — no `page.Close()`. default case: `page, err := os.Open(filepath.Join(fullpath, entry.Name()))` ... `buf, err := io.ReadAll(page)` — no `page.Close()`.
- **Verifier:** The code is real and current, and reachable: internal/manga/chapter_page_container.go:140 (GetMangaPageContainer) calls providerExtension.GetProvider().FindChapterPages(chapter.ID) on a live API path. Both unclosed Open() sites exist verbatim at the claimed lines. BUT the finding is half wrong and its stated impact is not provable:

(1) ZIP/CBZ branch (local.go:427-433) — REFUTED as an FD leak. zip.OpenReader opens exactly ONE os.File, and it IS closed (`defer r.Close()` at line 420). f.Open() returns a checksumReader over an io.SectionReader on that single shared fd — it allocates NO new file descriptor (the doc explicitly notes "Multiple files may be read concurrently" precisely because it section-reads one handle). Not closing it means only: the flate reader is not returned to archive/zip's sync.Pool, and CRC is not re-verified on Close. Both are GC-collected, in-memory, and per-call bounded. The finder's evidence for this branch describes a leak that cannot occur.

(2) Directory branch — the unclosed os.Open IS a real fd, and this half of the defect is genuine (it also leaks on the io.ReadAll error return). However, the claimed consequence — "accumulate open FDs until the process hits its descriptor limit ... failing across the whole server process" — is not provable. os.newFile installs a runtime finalizer/cleanup that closes the fd once the *os.File becomes unreachable, and `page` goes out of scope every iteration. This function io.ReadAll's every image into memory (hundreds of MB for a long-strip chapter), which is exactly the allocation pressure that forces GC — so the fds are reclaimed promptly. FindChapterPages is also self-limiting: it clears p.currentPages and closes p.currentZipCloser on every call, so only one chapter's worth of readers is ever live. Reaching an fd limit would require hundreds of pages opened with GC never running during a heavily-allocating loop.

Verdict: a real "opened and never closed" defect exists on ONE of the two claimed branches, reachable, but it is a correctness/hygiene bug relying on finalizers, not the descriptor-exhaustion outage the finder describes.
- **Fix:** Do NOT blanket-apply `defer page.Close()` to both branches as proposed. Two corrections:

(1) Directory branch — this is the only site worth fixing. Do NOT use `defer` inside the loop: defers do not run until FindChapterPages returns, so for a several-hundred-page chapter the deferred version holds every fd open for the whole function anyway (the exact condition the finding worries about) plus accumulates defer records. Extract the per-page read into a closure/helper so Close is scoped correctly, or close explicitly on all paths:

    page, err := os.Open(filepath.Join(fullpath, entry.Name()))
    if err != nil { return nil, fmt.Errorf("failed to open page: %w", err) }
    buf, err := io.ReadAll(page)
    _ = page.Close()          // close on BOTH paths, before the error return
    if err != nil { return nil, fmt.Errorf("failed to read page: %w", err) }

Note the finder's own proposal ("explicit page.Close() right after the io.ReadAll") still leaks on the io.ReadAll error return — close before checking err, as above.

(2) ZIP branch — optional hygiene only, not a leak fix. Same explicit-close-before-err-check shape, to return the flate reader to the pool. Discard the Close error (`_ = page.Close()`); do not propagate it (see regression_risk).
- **Regression risk:** Low but non-zero, and concentrated in how Close's error is handled rather than in the Close itself.

(1) Propagating the zip page Close() error would be the real regression. Chapters currently render from slightly-malformed CBZs because nothing inspects the reader's tail state on close. If a fix is written as `if err := page.Close(); err != nil { return nil, err }`, any archive whose flate stream close reports an error would flip from "loads fine" to "entire chapter fails to open" — a user-visible regression on files that work today. (The CRC/ErrChecksum path is surfaced by io.ReadAll, not Close, so ReadAll's existing error handling already covers real corruption; adding a checked Close only adds new failure modes.) Discard the Close error.

(2) `defer page.Close()` in the directory loop would hold every fd open until function return — for a several-hundred-page webtoon chapter that is strictly worse than today's finalizer-reclaimed behavior, converting a GC-mitigated non-issue into a guaranteed simultaneous-fd peak. This is the trap in the finder's proposed fix.

(3) Blast radius is otherwise contained: FindChapterPages has exactly one caller (GetMangaPageContainer, chapter_page_container.go:140). `buf` is fully materialized by io.ReadAll before any Close and is what's stored in loadedPage/ChapterPage.Buf and served to clients, so closing the reader after ReadAll cannot truncate or invalidate served page data. No other code holds the `page` reader.

#### MERG-MERGE-1 — Denshi (mpv-core) seek-bar OP/ED highlighting silently drops the fork's Intro/Outro + unlabeled-chapter heuristics that the actual skip logic uses — **open** (low, finder said medium)

- **Verdict:** CONFIRMED  |  **Category:** regression  |  **Slice:** merge-regressions
- **Location:** `seanime-web/src/app/(main)/_features/mpv-core/mpv-core-time-range.tsx:125`
- **What:** The merge ported the fork's OP/ED heuristics (19bed7eb: promote Intro/Outro-labeled and unlabeled ~90s chapters to opening/ending) into a shared media-core-chapters.ts module behind an opt-in `heuristics` flag, as documented in the merge commit message. It was correctly wired into the actual skip-execution path (mpv-core-player-inner.tsx:647, `{ guardIntro: false, heuristics: true, duration: durationRef.current }`) and into the web player's seek-bar (video-core-time-range.tsx:142, same options). But the new call added by this merge for Denshi's own seek-bar marker computation omits both `heuristics: true` and `duration`, even though `duration` is already an in-scope prop of the component (used elsewhere in the same file, e.g. lines 93/102/438).
- **Why it matters:** In Denshi, mpv-core-player-inner.tsx will correctly auto-skip an unlabeled ~90s chapter or an Intro/Outro-labeled chapter (heuristics on), but mpv-core-time-range.tsx's seek bar will never highlight that same region as a skip chapter (`skipChapters.includes(chapter)` in media-core-control-bar.tsx:768 requires the chapter object to appear in the heuristics-computed set, which without `heuristics:true` never includes Intro/Outro/unlabeled chapters). Users on Denshi playing releases with Intro/Outro-labeled or unlabeled OP/ED chapters will see auto-skip fire with no corresponding visual marker on the timeline — a user-visible inconsistency that the parity-matched code paths (web player, skip execution) do not have.
- **Evidence:** mpv-core-time-range.tsx:125: `const skipChapters = React.useMemo(() => getSkipChapters(chapters, skipPatterns, { guardIntro: false }), [chapters, skipPatterns])` — vs the sibling web component video-core-time-range.tsx:142: `getSkipChapters(chapters, skipPatterns, { guardIntro: false, heuristics: true, duration })`, and the actual Denshi skip logic mpv-core-player-inner.tsx:647: `const skipOpts = { guardIntro: false, heuristics: true, duration: durationRef.current }`.
- **Verifier:** Verified verbatim in current code. mpv-core-time-range.tsx:125 is exactly `getSkipChapters(chapters, skipPatterns, { guardIntro: false })` — no `heuristics`, no `duration` — while video-core-time-range.tsx:142 and mpv-core-player-inner.tsx:647 both pass `{ guardIntro: false, heuristics: true, duration }`. No guard rescues it: media-core-chapters.ts gates Intro/Outro promotion behind `heuristics &&` (lines 74-75) and skips the unlabeled-chapter Pass 2 entirely (line 83 `if (heuristics && ...)`), and the regex fallback is inert because mediaCoreDefaultPreferences.skipPatterns = "" (media-core-preferences.ts:25) so getRegexes returns []. Reachable and on by default: vc_highlightOPEDChaptersAtom defaults true (video-core.atoms.ts:190), aliased by mc_highlightOPEDChapters (mpv-core.atoms.ts:55). The includes() identity check is not a defeater: mpv-core-time-range computes getSkipChapters over the very array it passes as chapters={chapters} (:419) next to skipChapters={skipChapters} (:422), and control-bar:768 does `highlighted={highlightChapters && skipChapters.includes(chapter)}`. Both paths derive from identical source data — player-inner passes chapters={chapterCues} (:2003), the same chapterCues its heuristics-enabled skipChapters memo consumes (:634-664). So on Denshi an Intro/Outro-labeled or unlabeled ~90s chapter is auto-skipped but never highlighted.

TWO CORRECTIONS to the finder. (1) Provenance is wrong: line 125 was authored by UPSTREAM (5rahim, commit 09ab9d5f "feat(mediacore): skip patterns", reachable from origin/main and origin/next), not "added by this merge". The merge's fault is omission — failing to apply the fork's opt-in flag at a NEW upstream call site. (2) The regression is real but has a different shape, and the proposed fix is incomplete. Pre-merge (ac0b067b^1) media-core-control-bar.tsx:855 highlighted via `!!getChapterType(chapter.label) && highlightOPEDChapters` — covering Opening/Ending/Intro/Outro/RECAP regardless of length. So Intro/Outro chapters WERE highlighted before the merge and are not now (a true regression, stronger than the claimed inconsistency), but Recap chapters also lost highlighting and the proposed heuristics fix does NOT restore them: getChapterType returns "Recap" and heuristics only promotes Intro/Outro (via inSkipWindow) and unlabeled chapters to opening/ending. Conversely, unlabeled ~90s chapters were never highlighted pre-merge, so the fix ADDS behavior there rather than restoring it.
- **Fix:** The finder's fix is correct as far as it goes — change mpv-core-time-range.tsx:125 to `getSkipChapters(chapters, skipPatterns, { guardIntro: false, heuristics: true, duration })` with `duration` added to the dep array — mirroring video-core-time-range.tsx:141-144 and the fork-provenance comment style used there ("Fork (19bed7eb): opt into the Intro/Outro + unlabeled-chapter heuristics"). But note it does NOT fully restore pre-merge behavior: Recap-labeled chapters were highlighted pre-merge via getChapterType and remain unhighlighted after this fix. That is arguably correct — upstream deliberately redefined highlight as "this region will be skipped", and the fork already accepted that semantic in video-core — so no Recap change is needed, but it should be a conscious decision rather than an assumed side effect of "matching video-core". Separately out of scope: player-inner pushes synthetic AniSkip Opening/Ending chapters (:649-662) that are absent from chapterCues, so those can never highlight regardless of this fix.
- **Regression risk:** Contained. The edit touches one call site's options object, not the shared getSkipChapters in media-core-chapters.ts, so the other consumers (video-core-time-range.tsx:142, mpv-core-player-inner.tsx:663, and media-core-chapters.test.ts) are untouched — the `heuristics` flag is opt-in precisely so upstream's label-only contract and its tests stay green. Blast radius is limited to mpv-core seek-bar marker rendering.

Concrete risks: (1) False-positive highlights — with heuristics on, an unlabeled ~90s chapter in the first/last 20% that is NOT the OP (e.g. a cold-open or preview) gets promoted by pickSkipCandidate and turns blue. But mpv-core-player-inner.tsx:647 ALREADY auto-skips that exact chapter with the same options, so the highlight makes existing behavior more discoverable rather than less correct — it converges the seek bar onto the truth. (2) The synthetic single-chapter fallback at mpv-core-time-range.tsx:97-103 (`{label: null, start: 0, end: duration}` when chapterCues is empty) is now fed to the heuristics pass; it is safe because a whole-file chapter only enters inSkipWindow at 60-150s total runtime, which fails the `duration > SKIP_MAX_LENGTH * 2` (>180s) gate on line 83, so it can never be promoted. (3) Adding `duration` to the dep array is near-free: the memo already depends on `chapters`, which is itself recomputed on duration change (:124), so recompute frequency is effectively unchanged — no extra render churn during the 0-to-real duration transition on load.


### B2. Plausible (real-looking; reachability or impact not fully proven)

_Do not fix blind — each needs a reachability check first._

#### PLAY-MC-1 — mediacore.Coordinator permanently drops all events after a videocore client rebind (#814 fix doesn't propagate to Coordinator session tracking) — **open** (medium, finder said high)

- **Verdict:** PLAUSIBLE  |  **Category:** regression  |  **Slice:** playback-plane
- **Location:** `internal/mediacore/mediacore.go:379-398 (listenToBackendEvents) vs internal/videocore/videocore.go:940-958 (rebindClient call site)`
- **What:** videocore.go's #814 fix rebinds a dead client's playback state to a newly-reconnected client in place (`vc.rebindClient(eventClientID)`, videocore.go:956) without emitting a PlaybackLoadedEvent — it just mutates `playbackState.ClientId` so future pushed events carry the new client id. But mediacore.Coordinator.listenToBackendEvents only re-adopts a session when `c.session.Target == ""` AND the event is a *player.PlaybackLoadedEvent (mediacore.go:380-393). After a rebind, `c.session.ClientID` still holds the dead client's id and `c.session.Target` is non-empty, so every subsequent event's `key.ClientID` (the new, live id) mismatches `c.session.ClientID`, `isLoaded` is false, and the code takes the `else` branch: `c.mu.Unlock(); continue` — the event is dropped, forever.
- **Why it matters:** After a mid-playback disconnect+reconnect (exactly the scenario #814 was built to survive), videocore itself keeps working (subtitles, GetPlaybackState, client-facing sends), but mediacore.Coordinator silently stops: activePlaybackState/Status never update again, continuity/resume-position saves stop, Discord presence stops updating, auto-mark-watched-on-completion (CompletedEvent handler in SetupSharedEffects) never fires again, and no event reaches Coordinator subscribers (nakama, playlist, mediastream, plugin hooks) for the rest of that playback session. This is silent — no error, no log — the stream just looks like it plays but none of the Coordinator-driven side effects happen.
- **Evidence:** mediacore.go:379-398: `c.mu.Lock(); if c.session.Target != key.Target || c.session.ClientID != key.ClientID { _, isLoaded := ev.(*player.PlaybackLoadedEvent); if c.session.Target == "" && isLoaded { ...adopt... } else { c.mu.Unlock(); continue } }` — the only re-adoption path requires an EMPTY target, which a rebind never produces (target stays set, only ClientID silently drifted inside videocore).
- **Verifier:** The mechanism is real and the quoted code is accurate, but two load-bearing parts of the claim are wrong and reachability is unproven.

PROVEN: videocore.rebindClient (videocore.go:474-487) mutates vc.playbackState.ClientId in place and emits nothing. VideoCore.PushEvent (videocore.go:184-196) stamps every event via event.identify(state.PlaybackInfo.Id, state.ClientId, ...) — i.e. the rebound id. Adapter.toEvent (adapter.go:217-222) maps that to player.SessionKey{ClientID: ev.GetClientId()}. mediacore.listenToBackendEvents (mediacore.go:379-393) then mismatches (c.session.ClientID = dead id, c.session.Target non-empty) and takes the else branch `c.mu.Unlock(); continue`. The only re-adoption path does require c.session.Target == "", which a rebind never produces. The adapter is genuinely wired as a backend (core/modules.go:306, core/session.go:176) and Coordinator.Start spawns listenToBackendEvents per backend (mediacore.go:90-95), so this is not dead code. clearPlayback (videocore.go:458-463) pushes no TerminatedEvent, and no reaper clears playback on client death (GetClientIds appears only at videocore.go:953), so nothing resets the session mid-stream.

REFUTED — "permanently / forever": directstream/stream.go:474 calls Coordinator.Watch() on every stream start, which rebinds c.session with the fresh ClientID (mediacore.go:268-274). Impact is bounded to the remainder of the CURRENT playback; the next playback recovers on its own.

REFUTED — category "regression": pre-#814 the same event was dropped one layer earlier (the bare `continue` at videocore), so the Coordinator was equally deaf in that scenario. #814 did not introduce this; it is an incomplete fix, not a regression.

UNPROVEN — reachability: client ids persist in localStorage (seanime-web/src/lib/server/client-id.ts:57, getClientIdentity reuses the stored id), and eventClientID is the client's self-reported uuid, so a plain ws reconnect — the exact "mid-playback disconnect+reconnect" scenario the finder describes — reuses the SAME id and never rebinds. A page reload destroys the in-page player, so its first event is PlayerEventVideoLoaded, which takes the takeover branch (videocore.go:949-952), not rebind; and a genuinely new stream calls Watch() first, rebinding mediacore anyway. rebindClient requires a DIFFERENT client id sending a non-VideoLoaded player event while the bound conn is dead — I could not construct a concrete end-user path that produces that. Not dead code, but I cannot prove it fires in practice, so per the default I decline to CONFIRM.
- **Fix:** Do not adopt proposed fix (b) — relaxing the mediacore ClientID guard is actively unsafe (see regression_risk). If the rebind path is first shown to be reachable with a real repro, prefer keeping the repair inside videocore: have rebindClient emit a dedicated, adapter-mapped rebind event that the Coordinator handles as a narrow `c.session.ClientID = key.ClientID` update, gated on Target AND PlaybackID already matching the bound session — never a synthetic PlaybackLoadedEvent (which would re-fire load side effects across every subscriber). Simpler and cheaper: since Coordinator.Watch already rebinds the session per stream, the honest scope of this bug is "side effects lost for the rest of one playback" — verify reachability before spending anything on it.
- **Regression risk:** Both proposed fixes are riskier than the bug.

(a) "push a lightweight PlaybackLoadedEvent on rebind": the adapter maps VideoLoadedEvent -> player.PlaybackLoadedEvent, and mediacore's PlaybackLoadedEvent case (mediacore.go:406-409) calls populatePluginFields and resets activePlaybackState/activePlaybackInfo, then dispatch() fans out to EVERY Coordinator subscriber — nakama watch_room, playlist, mediastream, and plugin hooks. A synthetic load event mid-playback would read to all of them as a fresh playback start: re-triggered loading screens, nakama re-broadcasting stream identity to the room mid-episode, playlist advance logic re-evaluating, and videocore's own insight.go (which switches on VideoLoadedEvent, insight.go:152) double-counting a playback start. This turns a silent side-effect gap into visible cross-subscriber misbehavior on a path that currently works.

(b) "relax the Coordinator's mismatch handling to accept a ClientID change for the same Target": this is the dangerous one. That Target+ClientID equality check at mediacore.go:380 is the ONLY thing isolating per-user Coordinators, which are constructed per session with the same player.TargetVideoCore (core/session.go:176-177). videocore's client-event stream is explicitly broadcast to all per-session VideoCores (see the ownership comment at videocore.go:929-938). Relaxing the ClientID match would let one user's player events be adopted into another user's Coordinator session — corrupting activePlaybackState and, worse, writing updateContinuityState/RecordLastWatched resume positions and auto-mark-watched into the WRONG user's history (mediacore.go:447-449). That is silent cross-user data corruption on a networked/password-protected server, strictly worse than the missing side effects being fixed. Additionally, mediacore is deliberately target-agnostic and holds no wsEventManager, so the GetClientIds() liveness check the finder proposes mirroring is not even available at that layer without a new dependency.

#### DATA-DB-2 — Per-user CacheLayer instances all share one global AniList health/availability state — **open** (medium, finder said high)

- **Verdict:** PLAUSIBLE  |  **Category:** bug  |  **Slice:** data-user
- **Location:** `internal/platforms/shared_platform/cachelayer.go:26-28 (ShouldCache/IsWorking/AnilistClient), :35-45 (failureTracking), :252 (AnilistClient.Store), :328-387 (checkAndUpdateWorkingState)`
- **What:** `ShouldCache`, `IsWorking`, `AnilistClient` (atomic.Value) and `failureTracking` are package-level globals (the code even self-documents this: "devnote: I got lazy and used global variables"). `internal/core/session.go` builds one independent `CacheLayer` per user (via `userAnilistCacheDir`, correctly isolated on disk), but `checkAndUpdateWorkingState()` writes failures/`IsWorking` to the SAME global regardless of which user's `CacheLayer` called it, and `NewCacheLayer()` does `AnilistClient.Store(anilistClientRef.Get())` on every construction, so the background health-check goroutine in `init()` (which pings AniList every 10s via `anilistClient.BaseAnimeByID`) ends up probing with whichever user's client was constructed most recently.
- **Why it matters:** In multi-user mode, one user's expired/invalid AniList token (or a burst of errors on their account) can push the shared failure counter over `failureThreshold` and flip `IsWorking` to false for the ENTIRE server, forcing every other user's `CacheLayer` into cache-only mode even though their own tokens/accounts are fine — a cross-user availability/isolation bug. Symmetrically, the periodic recovery probe can be running against a different (possibly broken) user's client, giving false 'AniList API is back online' transitions server-wide.
- **Evidence:** `var ShouldCache = atomic.Bool{}` / `var IsWorking = atomic.Bool{}` / `var AnilistClient = atomic.Value{}` at package scope; `checkAndUpdateWorkingState` does `IsWorking.Store(false)` / `IsWorking.Store(true)` unconditionally on any `*CacheLayer` receiver; `NewCacheLayer`: `AnilistClient.Store(anilistClientRef.Get())`.
- **Verifier:** Structural claim CONFIRMED verbatim: cachelayer.go:26-28 really declares package-level `ShouldCache`/`IsWorking`/`AnilistClient` atomics plus `failureTracking` (:35-38) with the self-documenting "devnote: I got lazy and used global variables" comment; `NewCacheLayer` really does `AnilistClient.Store(anilistClientRef.Get())` at :252 (last-constructor-wins); `checkAndUpdateWorkingState` (:328-387) writes the globals unconditionally on any *CacheLayer receiver. Per-user instantiation is real and reachable: session.go:453-493 `buildUserSession` -> `anilist_platform.NewAnilistPlatform` (anilist_platform.go:43) -> `NewCacheLayer`, once per user session (sessions.GetOrSet at :406), and multi-user mode is live fork behavior (admin only short-circuits when Config.Server.Password == "", session.go:401). `go vet ./internal/platforms/shared_platform/` passes, so the init() prober is live code (`new(1)` is the Go 1.26 new(expr) form, not a typo).

HOWEVER the finder's headline harm mechanism is REFUTED by guards it did not read. checkAndUpdateWorkingState returns BEFORE addFailureRecord for exactly the user-specific error classes: 404 (:336) — which is what AniList returns for an expired/invalid token ({"message":"User not found","status":404}); 429 (:340) — rate limits are per-token, i.e. inherently per-user; and "user not found" (:346) which routes to the per-user logoutFunc and returns. So "one user's expired/invalid AniList token can push the shared failure counter over failureThreshold" is not what the code does — that path never records a failure. Errors that DO reach addFailureRecord (5xx/network/timeout) are AniList-wide conditions that would degrade every user anyway, which is arguably what a global IsWorking is for.

Two further mitigations bound residual impact: (1) clearFailureTracking() fires on ANY success from ANY user (:385), so crossing 4 failures needs a 30s window with no interleaved success server-wide; (2) when IsWorking==false, networkFirstGet still issues the request in a background goroutine and checkAndUpdateWorkingState(nil) flips IsWorking back to true (:517-526), so any healthy user's next request self-heals within seconds — which also makes the "stale prober client" sub-claim largely moot, since the 10s prober is redundant with that path.

What survives (hence PLAUSIBLE, not REFUTED): if AniList returns an unguarded user-specific error — e.g. HTTP 400 "Invalid token" for a revoked token, which contains neither "404"/"429" nor "user not found" — then 4 such errors in 30s from one broken user flip the whole server to cache-only: DeleteEntry (:1043) and UpdateMediaListEntryRepeat (:1025) hard-error for every user, and a WarningToast goes out via events.GlobalWSEventManager (:366) to everyone. I could not prove AniList's exact error string from code, so the reachability of the residual cross-user harm is unproven. Entry/progress updates are queued rather than lost (:968, :997), so there is no data loss on any path.
- **Fix:** Do NOT scope IsWorking per-CacheLayer — the global is correct for its actual purpose ("is the AniList API up"), which is a genuinely process-wide property, and per-instance scoping breaks the status/toggle handlers, leaks a goroutine per session, and removes the cross-user recovery path. Fix the real defect instead, which is the conflation of user-specific failures with API-wide failures inside checkAndUpdateWorkingState: classify the error before it reaches addFailureRecord and record ONLY API-wide classes (5xx, connection refused/reset, DNS, timeout), skipping all 4xx auth/permission responses rather than only the currently-hardcoded 404/429/"user not found" substrings. Concretely, invert the guard to an allowlist — record a failure only when the error is a transport error or a 5xx — so an unguarded 400 "Invalid token" from one revoked token can no longer degrade the server. Two adjacent cleanups worth folding in: (a) the "404" skip at :336 fires BEFORE the "user not found" branch at :346, and AniList's expired-token response carries status 404, so the per-user logoutFunc/ServerLoggedOutAnilist path at :347-350 is likely dead in practice — reorder the token check above the 404 skip; (b) drop the redundant AnilistClient global + 10s prober entirely (the background-retry path at :517-526 already restores IsWorking on any user's success), which removes the last-constructor-wins probe-with-a-random-user's-client wart the finder correctly identified without any per-user state. Gate any change behind a live multi-user repro first: the residual harm is unproven pending confirmation of what AniList actually returns for a revoked token.
- **Regression risk:** Larger than the finder implies. (1) internal/handlers/anilist.go:531 (HandleGetAnilistCacheLayerStatus) and :541 (HandleToggleAnilistCacheLayerStatus) read and toggle package-level shared_platform.IsWorking as a server-wide status + manual-override endpoint; scoping IsWorking per-CacheLayer breaks both — there is no well-defined answer to which user's instance a status GET should report, and the toggle (a debugging/offline-simulation affordance) would silently stop affecting anything. Both are on the generated API surface (seanime-web/src/api/generated/), so a signature change ripples into the web UI and Tenji's hand-synced types. (2) internal/core/modules.go:609 drives ShouldCache from the admin's global settings (settings.Anilist.DisableCacheLayer) — it is global by design, and a per-user split would need a new plumbing path from admin settings into every session. (3) Per-instance health probes would spawn one 10s-ticker goroutine per user session; sessions are cached in a.sessions (session.go:406) and never torn down, so this trades a transient server-wide degradation for an unbounded permanent goroutine leak. (4) The fast-recovery property in reason() depends on the CURRENT global: today one healthy user's success clears failures and restores network-first mode for everyone, including for a user whose own cache is cold. Per-user scoping removes that cross-user rescue — a user who hits 4 failures would stay stuck in cache-only until their OWN next success, which on a cold cache means "no cached data available" errors instead of live data. Fixing isolation could measurably worsen the common single-AniList-outage case.

#### TORR-TS-2 — builtin torrent client's per-second scheduler holds the global write lock across blocking os.Stat syscalls for every torrent — **open** (low, finder said medium)

- **Verdict:** PLAUSIBLE  |  **Category:** perf  |  **Slice:** torrentstream
- **Location:** `internal/torrent_clients/builtin_client/builtin.go:1266-1323 (runScheduler / sampleRates)`
- **What:** runScheduler ticks every second and calls sampleRates(now), which takes `c.mu.Lock()` (a write lock, not RLock) at the top of the function and holds it via `defer c.mu.Unlock()` for the entire loop body, which includes an `os.Stat(entry.model.Destination)` filesystem syscall per tracked torrent.
- **Why it matters:** c.mu guards every other Client operation (AddMagnet, RemoveTorrent, TorrentExists, Snapshots, PauseAll, etc. all take c.mu.Lock()/RLock()). Doing a blocking disk syscall per torrent, once per second, while holding that same lock stalls every concurrent API call on the torrent client for as long as the stats collide with slow/contended storage (network share, antivirus scanning on Windows, spun-down external disk, or simply many active torrents). This scales linearly with torrent count and runs forever in the background — a self-inflicted lock-contention bottleneck on the lifecycle path this audit is targeting (add/remove/pause all block on it).
- **Evidence:** builtin.go:1281-1284: `func (c *Client) sampleRates(now time.Time) { defer util.HandlePanicInModuleThen(...); c.mu.Lock(); defer c.mu.Unlock(); for _, entry := range c.torrents { ... }` followed by, inside the loop, `if _, err := os.Stat(entry.model.Destination); err != nil { ... }` (line 1310).
- **Verifier:** The quoted code is real and current, not hallucinated. builtin.go:48-49 confirms `mu sync.RWMutex`; sampleRates (1280-1322) takes `c.mu.Lock()` + `defer c.mu.Unlock()` and calls `os.Stat(entry.model.Destination)` at line 1309 (finder said 1310, off by one) inside the per-torrent loop. runScheduler (1265-1278) ticks every 1s and is started unconditionally via `go c.runScheduler()` at builtin.go:223 in New(). It is reachable in production, gated on `settings.Torrent.Default == torrent_client.SeanimeClient` (internal/core/modules.go:771) — a real user option, not dead code. The finder's cited precedent is also real: reconcileQueue (1104-1111) does RLock -> copy entries -> RUnlock -> work.

But I could not prove the claimed impact, so this is a code smell rather than a demonstrated defect:
(1) `Destination` is the download directory, which is the SAME path for essentially every torrent. After the first os.Stat the rest hit the OS dentry cache at sub-microsecond cost. "Scales linearly with torrent count" is arithmetically true but each unit is ~free, and torrent counts are single/double digit.
(2) It is not distinguishing behavior: setPaused at builtin.go:580 also calls `os.Stat(entry.model.Destination)` while holding `c.mu.Lock()` (it only unlocks at 581 on the error branch). The same sampleRates loop also calls `entry.torrent.Stats()`, which takes anacrolix's internal locks under c.mu — comparable blocking the finding never mentions.
(3) The impact ceiling is a sub-millisecond stall of Snapshots/UI polling — no crash, no corruption, no data loss. The pathological case (hung NFS/SMB mount) would freeze the builtin client API, but on such a mount setPaused, RemoveTorrent's file deletion, and the torrent storage layer are already blocked. That is a broken deployment, not a defect this code introduces.

Additionally, the finder's proposed fix as written is incorrect (see corrected_fix): it introduces a genuine data race that the current code does not have.
- **Fix:** The finder's fix is wrong as written and must not be applied verbatim. "Snapshot the entries slice under the lock, release it, then do the os.Stat calls outside the lock" reads `entry.model.Destination` off-lock, but MoveStorage WRITES `entry.model.Destination` under `c.mu.Lock()` at builtin.go:870 and builtin.go:908. That is a straight data race the Go race detector would flag — the current code is race-free precisely because the stat happens under the lock.

A correct minimal version, if this is judged worth touching at all:
1. Under `c.mu.Lock()`, do only the in-memory work that genuinely needs it (Stats() deltas, downSpeed/upSpeed/lastSample/lastDownload/lastUpload, the model.Name backfill) and COPY each entry's `model.Destination` string into a local slice alongside the *torrentEntry pointer.
2. Release the lock, then os.Stat the copied destination strings. Deduplicate them first — they are almost always the same directory, so N stats collapse to 1.
3. Write results back via `entry.setWriteError(...)` / `entry.getWriteError()`, which need NO change and can already be called off-lock: torrentEntry has its own `writeErrorMu sync.RWMutex` at builtin.go:76 guarding exactly that field.

Given the measured cost is a cached stat on a repeated path, my recommendation is to leave it alone, or at most apply the dedup in step 2 (which removes the linear-scaling claim entirely without any locking change).
- **Regression risk:** Concrete and non-trivial — the shared lock protects real invariants:

1. DATA RACE ON model.Destination (the big one). Moving os.Stat off-lock means reading `entry.model.Destination` without c.mu, while MoveStorage writes that exact field under c.mu.Lock() at builtin.go:870 and 908. Result: a torrent moved via MoveStorage concurrently with a scheduler tick can be stat'd against a torn/stale path — producing a SPURIOUS "save directory not found" write-error on a torrent whose directory is perfectly fine. That write-error surfaces to the UI via Snapshots (982) and is currently impossible.

2. LOST-UPDATE ON RATE FIELDS. If the fix follows the stated "re-acquire the lock only to write back the computed rates", the rates are computed against a snapshot taken before the gap. MoveStorage at 906-914 re-acquires c.mu and resets `entry.lastSample = time.Now()` and swaps `entry.torrent = readded`. A write-back after that gap would clobber the fresh lastSample with a stale one and compute the next tick's delta across a torrent object that was Drop()ed and re-added — yielding garbage/negative speeds. The current single-critical-section design makes this atomic.

3. ENTRY LIFETIME. removeRuntime (470) takes c.mu.Lock() and mutates c.torrents. Holding *torrentEntry pointers across a lock gap means writing rates/write-errors into an entry already removed by RemoveTorrent (418) — harmless memory-wise (GC'd pointer) but it can resurrect state, and any write-back that re-reads c.torrents[hash] must nil-check what the current code never has to.

4. STALE-STATE CLEARING. The else-branch at 1315-1319 clears a "save directory"-prefixed write error. Off-lock, this can race the setPaused path at 580-584, which sets its own directory error — the clear could erase an error setPaused just legitimately set, silently re-enabling a resume against a missing directory.

Blast radius traced via c.mu users in builtin.go: Start(248), Close(254), AddMagnet(318/360/405), TorrentExists(412), RemoveTorrent(421), removeRuntime(470), setPaused(553/594), SetForceStart(635), MoveQueue(656), file-priority paths(706/730/752), MoveStorage(846/869/906), Snapshots(982), SnapshotsOrder(1038), getEntry(1060), reconcileQueue(1106/1111), filePrioritiesSnapshot(1244), compactQueue(1326). Every one of these currently relies on sampleRates' critical section being atomic. Trading a sub-microsecond cached stat for a real race across that surface is a net negative.

#### PLUG-PLG-5 — $await blocks with no timeout, unlike the 30s-bounded promise wait used elsewhere — **open** (low, finder said medium)

- **Verdict:** PLAUSIBLE  |  **Category:** bug  |  **Slice:** plugin-runtime
- **Location:** `internal/util/goja/async.go:34-57 (BindAwait), 12-32 (WaitForPromise); bound at internal/extension_repo/goja_plugin.go:260`
- **What:** `BindAwait` exposes `$await` to plugin JS (bound on the main plugin loader VM, goja_plugin.go:260) using `WaitForPromise(context.Background(), promise)` (async.go:39) — an unbounded context. `WaitForPromise` itself (async.go:12-32) just polls `promise.State()` every 10ms with no deadline unless the caller supplies one. This is inconsistent with `handlePromiseResult` (goja_plugin.go:447-455), which explicitly wraps the same kind of wait in a 30-second `context.WithTimeout` specifically to 'force stop' runaway waits.
- **Why it matters:** Any plugin JS that does `$await(somePromiseThatNeverSettles(...))` (e.g. awaiting a webview/user-input promise that the user never resolves, or a promise chained off something outside fetch's own 35s HTTP timeout) blocks the calling goroutine — and holds its checked-out goja.Runtime — indefinitely, with no way to reclaim it (compounds PLG-2: no Interrupt() is ever fired to break out of this wait either).
- **Evidence:** async.go:36-39: `vm.Set("$await", func(promise goja.Value) (goja.Value, error) { ... WaitForPromise(context.Background(), promise) ... })` — contrast with goja_plugin.go:449 `ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)` used for the equivalent wait elsewhere.
- **Verifier:** Code is real and current, but the finding is mislocated and over-framed. VERIFIED: async.go:36-56 does call WaitForPromise(context.Background(), promise); WaitForPromise (12-32) is a bare `for promise.State()==Pending { select{<-ctx.Done()...; default:}; time.Sleep(10ms) }` with no intrinsic deadline; handlePromiseResult (goja_plugin.go:447-455) really does wrap the same call in a 30s context.WithTimeout with the comment "force stop after 30 seconds". So the inconsistency the finder cites exists.

MISLOCATED: BindAwait is NOT bound on "the main plugin loader VM". BindPluginAPIs (which contains the BindAwait call at line 260) is invoked at goja_plugin.go:181 (each pooled hook runtime, via GetOrCreatePrivatePool) and :200 (the UI VM). p.loader never receives $await.

MISSED CONTEXT that cuts against the framing: two OTHER WaitForPromise call sites are also unbounded — goja_base.go:186 (awaitAndExportValue passed context.Background()) and episode_tab.go:369 (WaitForPromise(context.Background(), promise)). The 30s bound in handlePromiseResult is the exception, not an established norm $await deviates from.

STRONGEST point, which the finder undersold: vm.Interrupt() genuinely cannot break this wait — time.Sleep in Go never returns to goja's bytecode dispatcher, so the interrupt flag is never polled. An infinite JS `while(true){}` IS recoverable via Interrupt; a stuck $await is not. ClearInterrupt (85-121) doesn't even try: `p.loader.ClearInterrupt()` CLEARS goja's interrupt flag, it does not set one.

WHY NOT CONFIRMED: I could not find any in-tree binding that leaves a promise unsettled. fetch settles within its timeout, and BindFetch (fetch.go:192-201) drains vmResponseCh on a SEPARATE goroutine, so resolve() fires even while the caller's goroutine is parked in the poll loop — $await(fetch(...)) works and is bounded (defaultTimeout 35s, fetch.go:24). rejectNow (app_settings.go:614-618) discards resolve but calls reject unconditionally on the next line, so it always settles. The trigger requires plugin-authored JS such as $await(new Promise(() => {})). Real code shape, unproven real-world path.
- **Fix:** Do not copy the 30s bound into $await. The actual gap is not "no deadline" but "no reclaim path": the wait is a Go sleep loop, so vm.Interrupt() can never break it and ClearInterrupt (goja_plugin.go:85-121) has no way to free the goroutine or its pooled runtime on plugin unload. Fix by threading a per-plugin cancellable context (cancelled from ClearInterrupt, alongside the existing p.store.Stop()/p.storage.Stop()/DeletePluginPool teardown) into BindAwait's closure in place of context.Background(), leaving the normal timeout unbounded so long fetches with custom {timeout:N} keep working. Optionally add a long safety deadline (well above fetch's 35s default and any plugin-set timeout) plus a warn log naming the extension id, rather than 30s. If the poll loop is touched at all, prefer replacing the 10ms time.Sleep with select on ctx.Done() plus a ticker so cancellation is observed promptly instead of up to 10ms late. Same treatment applies to the two sites the finder missed (goja_base.go:186, episode_tab.go:369).
- **Regression risk:** The finder's proposed blanket 30s timeout would break currently-working behavior. fetch.go:318-322 lets plugin JS pass a per-request `{timeout: N}` which is cloned into the request at fetch.go:437 with no upper clamp — so `$await(fetch(url, {timeout: 120}))` works today and would begin returning context.DeadlineExceeded at 30s under the fix. The same blanket bound would also break legitimate long $await chains (multiple sequential fetches, scheduler-driven promises) and would silently change $await's semantics away from JS `await` (which has no timeout), per the API's own doc comment at async.go:34-35. Blast radius of touching WaitForPromise itself is wider than $await: it is also called by goja_base.go:198 (awaitAndExportValue — every manga/anime-torrent/onlinestream/custom-source provider result), goja_plugin.go:452, and episode_tab.go:369; changing the shared helper's default would silently bound all provider result waits too.


### B3. Unverified (low severity, passed through without a verify pass)

#### TORR-TS-4 — TorrentStatus.UploadProgress is computed as a self-referential delta and never converges to a meaningful value — **open** (low)

- **Verdict:** UNVERIFIED  |  **Category:** bug  |  **Slice:** torrentstream
- **Location:** `internal/torrentstream/client.go:219-227 (the background status-reporting goroutine in initializeClient)`
- **What:** Each tick, the new TorrentStatus struct's UploadProgress field is computed as `(&bytesWrittenData).Int64() - c.currentTorrentStatus.UploadProgress`, i.e. cumulative-bytes-written-so-far minus the *previous tick's UploadProgress value* (which was itself already a subtraction). Unlike DownloadProgress, which is simply the cumulative `f.BytesCompleted()`, this does not compute a stable delta or a cumulative total — it recurses on itself each tick (U_n = W_n - U_{n-1}), producing an oscillating/meaningless number instead of either 'total bytes uploaded' or 'upload rate'.
- **Why it matters:** The reported uploadProgress stat is garbage after the first couple of ticks (it does not track actual bytes uploaded). It's currently only surfaced through the generated API types with no frontend consumer found, so today it's latent rather than user-visible, but it is a real logic defect that will misbehave the moment any client starts reading it, and it's inconsistent with how the sibling DownloadProgress field is computed in the same struct literal.
- **Evidence:** client.go:219-227: `c.currentTorrentStatus = TorrentStatus{ Size: size, UploadProgress: (&bytesWrittenData).Int64() - c.currentTorrentStatus.UploadProgress, DownloadSpeed: downloadSpeed, UploadSpeed: uploadSpeed, DownloadProgress: downloadProgress, ... }` where `c.currentTorrentStatus.UploadProgress` on the right-hand side is the previous tick's already-corrupted value.
- **Fix:** Mirror DownloadProgress: set UploadProgress to the cumulative `(&bytesWrittenData).Int64()` directly (the code already tracks `c.lastBytesWrittenData` separately for the upload-speed delta, so there's no need to reuse the display field as an accumulator).

#### WEBU-WEB-2 — Loading-screen artwork fade-in gate (backdropLoaded/logoLoaded) isn't reset across media/artwork changes when the overlay stays mounted — **open** (low)

- **Verdict:** UNVERIFIED  |  **Category:** bug  |  **Slice:** web-ui
- **Location:** `seanime-web/src/app/(main)/_features/video-core/video-core-loading-screen.tsx:71-78`
- **What:** backdropLoaded/logoLoaded are component-local useState with no effect resetting them when `media`/`artwork` changes. The parent overlay div only unmounts VideoCoreLoadingScreen once hasPlayback has been true for 800ms (video-core.tsx:420-428); if the user switches to a different show while still in a loading state (hasPlayback never became true), the same VideoCoreLoadingScreen instance persists with backdropLoaded/logoLoaded still `true` from the prior artwork, so artworkReady is true immediately for the new artwork/backdrop before the new image has actually loaded.
- **Why it matters:** The gate's entire purpose ('show artwork only when all requested images are loaded') is bypassed for the second artwork shown in a given mount — new backdrop/logo can appear at full opacity mid-load (blank/partial paint) instead of fading in only once ready. Narrow trigger (switching shows during an in-progress load, not simple episode-to-episode within the same show since mediaId is unchanged there), but the code path is real.
- **Evidence:** const [backdropLoaded, setBackdropLoaded] = React.useState(false)
const [logoLoaded, setLogoLoaded] = React.useState(false)
...
const artworkReady = hasArtwork && backdropLoaded && (artwork?.logo ? logoLoaded : true)
- **Fix:** Reset backdropLoaded/logoLoaded in a useEffect keyed on mediaId (or artwork?.fanart/artwork?.logo), or key the component by mediaId at the call sites so React remounts it fresh per media.

#### WEBU-WEB-3 — entry-preloader's warmedAt Map grows unbounded for the life of the SPA session — **open** (low)

- **Verdict:** UNVERIFIED  |  **Category:** leak  |  **Slice:** web-ui
- **Location:** `seanime-web/src/lib/entry-preloader.ts:33,205,250`
- **What:** warmedAt is module-level state that records a timestamp per preloaded/hovered entry (`type:id`) and is only ever written to, never deleted or capped. The other two module maps are bounded: `queued` evicts its oldest entry once it hits MAX_QUEUED_PRELOADS (24), and `inFlight` self-cleans in runEntryPreload's finally block. warmedAt has no equivalent.
- **Why it matters:** Over a long browsing session (hovering/opening many library/discover cards), warmedAt accumulates one entry per distinct anime/manga id ever touched, for as long as the SPA tab stays open — a slow, unbounded memory leak. Individually cheap (string key + number), but with no ceiling.
- **Evidence:** const warmedAt = new Map<string, number>()
...
warmedAt.set(key, Date.now())  // runEntryPreload, no corresponding delete anywhere in the file
- **Fix:** Cap warmedAt's size (evict oldest, same pattern as `queued`) or lazily prune entries older than ENTRY_PRELOAD_STALE_TIME on each read/write.


### B4. Refuted — claimed but disproven (recorded so they are not re-chased)

- **NAK-1** <nakama> Room payload still lacks stream identity for file/torrent/onlinestream (F8, still open)
  - _Why refuted:_ The code quotes are accurate — RoomPlaybackStatusPayload (watch_room.go:160-180) has no LocalFilePath/OnlinestreamParams/TorrentStreamParams, and RelayPlaybackStatus (watch_room.go:811-816) populates room.CurrentMediaInfo with only MediaId/EpisodeNumber/AniDBEpisode/StreamType, so the three params fields declared at watch_party.go:192-197 stay zero-value for rooms (the only populators, hostPlaybackHandleStatus and handleWatchPartyRelayModeOriginStreamStartedEvent, are in the legacy watch-party plane). But the claimed IMPACT is disproven. The harmful path is gated at BOTH client entry points: useRoomStreamJoin (nakama-room-sync.ts:529-534) sets canJoin=false unless streamType is debrid|torrent and join() early-returns on the same check, with a comment naming this exact case and citing the prior audit ID — "Only debrid/torrent are (re)joinable ... A 'file'/onlinestream controller stream has nothing a peer can open, and falling through to the debrid endpoint would kick off an unrelated auto-select (F19)"; and the auto-follow path (nakama-room-sync.ts:205-206) logs "Cannot auto-follow stream type" and returns for the same set. F19 — the dangerous half of this cluster — was FIXED. A follower never gets a dead player or an unrelated auto-select for file/onlinestream; the Join button simply never renders. That directly contradicts the finder's "root of the recurring 'follower gets no live player / sync applies to nothing' user reports." RoomStreamInfo (watch_room.go:884-891) being identity-only + ControllerUserID confirms the intended contract is "reuse the controller's debrid selection", not "ship the controller's file path" — i.e. rooms deliberately do not support file/onlinestream. This is an unimplemented, guarded feature gap, not a latent HIGH defect. The only surviving kernel is far narrower than claimed: torrent join calls torrentStart.handleAutoSelectStream({mediaId, episodeNumber, aniDBEpisode}) (line 537), an independent selection — but auto-select runs server-side against global torrentstream settings, so both clients normally converge on the same release; divergence requires search ranking to shift between the two calls. Separately, the join handler (nakama_rooms.go:312-314) does fall through to opts.AutoSelect=true on the DEBRID StartStream for any non-debrid type, but that is unreachable from the shipping UI due to the client gate and is membership-gated (line 247) — a latent trap for a future caller, not a live bug.
- **NAK-2** <nakama> Legacy watch-party debrid proxy still shares one singleton CDN link across every peer (F14, still open)
  - _Why refuted:_ The finding is a verbatim restatement of audit/nakama-2026-07-13.md F14 (lines 131-138), reproducing its errors without re-verification. Three load-bearing claims fail against code I read:

(1) "singleton CDN link" is false. GetStreamURL (internal/debrid/client/repository.go:502-513) is not a singleton field — it ranges the PER-USER `r.streamManagers` result.Map, sorts keys, and returns the first non-empty URL. It hands back an arbitrary (lowest-uid) user's stream, which is a cross-user leak, not one link shared by design.

(2) "per-peer GetUserStreamShare the room engine uses to avoid exactly this contention" is false — this is the finding's central contrast and it does not exist. GetUserStreamShare(userID) (repository.go:537-563) is called with `info.ControllerUserID` at nakama_rooms.go:279 and :293 — it returns the CONTROLLER's share, identical for every follower. It is per-controller, not per-peer. Contention avoidance is not in that function: it is in the caller (nakama_rooms.go:306-311), which passes SharedTorrentItemId+FileId so each peer resolves its own fresh link. And at nakama_rooms.go:310 the room engine ITSELF falls back to `opts.Torrent.StreamUrl = share.StreamUrl` — sharing the raw link verbatim — when no torrent item exists. So swapping in GetUserStreamShare would not, by itself, change link-sharing behavior at all.

(3) The claimed harm is contradicted by the repo's own measurement. prewarm-audit.md:237 states TorBox does not track IP, links are not IP-locked, one link plays on multiple devices concurrently ("confirmed in practice"), and that the follower-never-loads symptom was a Nakama cross-platform bug, not a URL limit — explicitly calling the contention comment "misleading and should be corrected." The finder propagated that misleading comment as its impact rationale.

The proposed fix is also incoherent: these are cross-server /host/ endpoints. HandleNakamaProxyStream (nakama.go:413-434) runs on the PEER's server and calls the HOST over HTTP with X-Seanime-Nakama-Token. The host has no local user id for a remote peer, so GetUserStreamShare(peerUserID) would always miss the map and return a permanent 404.

Minor factual drift: routes are at routes.go:625-627, not 626-628. Reachability is the inverse of the claim — the finder undersold it (the unused useNakamaCreateWatchParty hooks are irrelevant); the live chain HandleNakamaProxyStream -> nakama.go:425 -> :372 reaches it without them.

A real but DIFFERENT defect does exist on these lines (cross-user stream leak via the sorted-map scan), already noted by a prior verifier in audit/.raw-run2.json:205. I am refuting the finding as written — its mechanism, its impact, and its fix are all wrong — not the existence of any issue at this location.
- **NAK-4** <nakama> Idle-room reaper still deletes a room without notifying its members
  - _Why refuted:_ The quoted code is real and current, but the defect is not. Three independent proofs: (1) reapIdleRoomsWith only reaps a room after proving NO participant's ClientID is in the live set (watch_room.go:1300-1321; live = wsEventManager.GetClientIds() at :1290). The proposed fix would collect exactly the client IDs proven not connected — a provable no-op. (2) Those ClientIDs are already empty: HandleClientDisconnect sets p.ClientID = "" for every dropped client (watch_room.go:529-535), with an explicit comment (:526-528) that this stops it counting "as a broadcast or promotion target". The host-leave path the finder holds up as the model filters on precisely this (`k != userKey && p.ClientID != ""`, :466), so collecting from a reaped room yields an empty slice. (3) SendEventTo (internal/events/websocket.go:382-406) matches conn.ID against live m.Conns and writes only to matches — there is no offline queue, so an event emitted at reap time can never reach a client reconnecting 2min later. The finder's own scenario is chronologically unreachable by his own fix. Moreover the claimed symptom is ALREADY fixed by the correct mechanism: internal/handlers/nakama_rooms.go:41-52 maps ErrRoomNotFound/ErrParticipantUnknown to a typed HTTP 404, whose comment names the exact symptom ("so the client can react (clear a stale room on 404 instead of tight-looping a logged 500)") — that is the 2026-07-14 audit's typed-404 fix. LeaveRoom is idempotent for the same reason (:445-450). The finder pattern-matched "close path sends event, reap path doesn't" as asymmetry without checking the two paths have OPPOSITE liveness preconditions: host-leave fires while others are still connected; reap fires only once nobody is. The finder also treats ErrRoomNotFound as the bug when it is in fact the delivery vehicle for the close signal.
- **TS-3** <torrentstream> AddMagnet has a check-then-act race that can double-add the same torrent and leak a storage closer
  - _Why refuted:_ The check-then-act STRUCTURE is real and correctly quoted (builtin.go:319-324 RLock/read/RUnlock, then unlocked c.database.GetLocalTorrents() at :326, then addPersisted's c.mu.Lock() insert at :406-408), and callers are genuinely unserialized (HandleTorrentClientAction per-request goroutine, autodownloader.downloadTorrent, plugin/other.go's bare `go func(){ AddMagnets(...) }`). But every claimed CONSEQUENCE is disproven by code:

(1) NO STORAGE LEAK — the headline harm is false. `newTorrentStorage` (builtin.go:237) returns a resource-free value struct on BOTH platforms, whose Close() is a literal no-op:
  - desktop: `storage.NewFileOpts(...)` -> `&fileClientImpl{opts}` (5rahim/torrent@v0.1.3/storage/file-client.go:65). Its Close() is `return me.opts.PieceCompletion.Close()` (file-client.go:67-69), and seanime passes `noClosePieceCompletion{pc}` whose Close() is `return nil` (builtin.go:30-32). So Close() == return nil.
  - mobile: `newClassicFileStorage` -> `&classicFileStorage{baseDir, pc}` with `func (s *classicFileStorage) Close() error { return nil }` (classic_storage.go:46-48).
  File handles/dirs are created ONLY inside OpenTorrent (file-client.go:70+, classic_storage.go:50+), which is never called on the loser's storage. The dropped closer holds zero OS resources and is GC'd. "Leaking whatever file handles/resources that storage.ClientImplCloser held" describes resources that do not exist.

(2) NO DOUBLE-ADD — `AddTorrentSpec` -> `AddTorrentOpt` (client.go:1568) takes `cl.lock()`, finds `cl.torrentsByShortHash[infoHash]`, and returns the EXISTING torrent with new=false. The library dedupes under its own lock; that is what the `new bool` return exists for. `MergeSpec` is explicitly documented "Note that any `Storage` is ignored" (client.go:1642), so the second spec's storage is discarded by design.

(3) RESIDUAL EFFECTS BENIGN — both entries wrap the SAME *anacrolix.Torrent pointer and are constructed from an identical `item` (same hash/magnet/destination). UpsertLocalTorrent is hash-keyed so the second write is idempotent-by-key, and AddMagnet calls reconcileQueue() afterward. The SetOnWriteChunkError rebind points at the entry that actually won the map, so it is consistent rather than corrupt.

The finder reasoned outward from the shape of the check-then-act pattern to an assumed impact without reading newTorrentStorage's return type or the anacrolix AddTorrentOpt/MergeSpec contract. Both refute the stated harm. What is left is a structurally-untidy but observably harmless race.
- **PLG-3** <plugin-runtime> No recover() at the goja→Go boundary for hook dispatch — a raw Go panic in a binding crashes the whole process
  - _Why refuted:_ The mechanism is real but the defect is not. What I confirmed: goja does re-panic non-Value panics out of RunProgram (exceptionFromValue vm.go:5806-5814 `default: return nil` -> handleThrow vm.go:840-842 `panic(arg)` -> RunProgram runtime.go:1447-1456 `else { panic(x) }`), and bindHooks' MakeFunc handler (goja_plugin.go:358-396), Manager.Run (goja_runtime_manager.go:104-116) and Hook.Trigger (internal/hook/hook.go) do lack a local recover. Every claim that turns that into a defect is false:

(1) EVIDENCE IS BROKEN. This codebase does not use bare `recover()`; it uses `util.HandlePanicInModuleThen/ThenS/WithError/HandlePanicWithError` helpers (internal/util/panic.go:28-70), each a recover. 253 non-test uses across internal/. The finder's literal `recover()` grep missed all of them -- including the plugin load path itself, guarded at goja_plugin.go:134 (`defer util.HandlePanicInModuleThen("extension_repo/NewGojaPlugin", ...)`), which covers the RunString(source)/RunString("init();") hook-registration path at :226/:234.

(2) CLAIMED TRIGGER DOES NOT EXIST. "several `.Export().(T)` unchecked type assertions exist across the goja_bindings package" is false -- every `Export().(` in internal/goja/goja_bindings/ is in a _test.go file. Production bindings panic deliberately with `vm.ToValue(...)`/`vm.NewTypeError(...)` (bindings.go:23-32, crypto.go, crypto_encoders.go), which exceptionFromValue handles via `case Value`/`case *Object` and converts to JS exceptions. That is the designed contract, not a hazard.

(3) ALL FOUR NAMED UNRECOVERED PATHS ARE GUARDED, before the trigger, on the same goroutine: scanner -- hydrator.go:149 HandlePanicInModuleThenS is at the top of the per-file goroutine body ahead of the triggers at :167/:178, plus scan.go:59 and matcher.go:224; autodownloader -- checkForNewEpisodes guarded at :296 before its trigger at :324, plus :117/:231 (triggerRunCompleted:403 is called on the guarded goroutine); debrid -- downloadTorrentItemThen:146 / downloadFile:277 guard the launchDownloadLoop goroutine that reaches the triggers at download.go:567/:593; cron -- triggers ZERO hooks at all (grep of internal/cron/ for `.Trigger(` is empty), so that call site is fabricated.

The actual residual is inverted from the claim: a plugin panic does not terminate the process, it unwinds to the nearest HandlePanic guard on that goroutine and aborts the enclosing operation (one scan group / one download) with a logged stack trace via printRuntimeError. That is a localized collateral-blast-radius nit, not a high-severity process crash.

Incidental real bug found, unrelated to this finding: console.go:56-64's recover is in the factory `logFunc(t string)`, not in the returned closure, so it only guards construction of the function and never an actual console.log invocation -- a dead guard.
- **NAK-1** <media-sources> Queue.runNext() called with inconsistent locking, plus mutex held across a 5s sleep and a blocking channel send
  - _Why refuted:_ The quoted lines are real and current, but the headline race is unreachable and the stated impacts are false.

1) The unlocked goroutine never spawns. Add() gates it: `if runNext && q.active { go q.runNext() }` (queue.go:84-87). `runNext` = `opts.StartNow`, passed at chapter_downloader.go:143. The ONLY non-test caller of AddToQueue is internal/manga/download.go:211, whose DownloadOptions literal sets only DownloadID and Pages — StartNow is omitted, so it is always false. `manga.DownloadChapterOptions.StartNow` (download.go:76) is written by the HTTP handler (handlers/manga_download.go:38) and then never read anywhere — it is silently dropped at the manga → chapter_downloader boundary. Both tests pass `StartNow: false` explicitly. Therefore no second, unlocked runNext ever runs concurrently with the locked ones: no data race on q.current, and `go test -race` cannot observe one.

2) No deadlock / blocking send. runCh is `make(chan *QueueInfo, 1)` (chapter_downloader.go:92) with a single consumer: the Downloader.Start() goroutine — which is the SAME goroutine executing cd.run → downloadChapterImages → queue.HasCompleted (chapter_downloader.go:268) → runNext → send. The buffer is provably empty at that moment (the item was already received), and Run()'s runNext short-circuits on `q.current != nil`, so nothing else can fill it. The send at queue.go:209 never actually blocks.

3) The claimed user-visible impact is fabricated: `Queue.GetCurrent()` (queue.go:213) has ZERO callers in the repo, so "download UI frozen on GetCurrent" cannot happen.

Residual truth: the 5s sleep (queue.go:204) does execute with q.mu held via HasCompleted()/Run(), so Add/Run/Stop (reachable from HTTP handlers) can block ~5s once per chapter transition. That is a real but minor latency wart on a deliberately throttled path (`// TODO: This is a temporary fix to prevent the downloader from running too fast.`), not a high-severity race. Line 86 is a latent landmine only if someone later wires StartNow through.
- **WEB-1** <web-ui> Continuity resume-seek gate compares a 0-1 ratio to 90, so it never rejects near-finished episodes
  - _Why refuted:_ The quoted code is real and current — continuity.hooks.ts:113 does compare a [0,1] ratio to 90, and the sibling getEpisodePercentageComplete:46 correctly does `* 100` first. The unit mismatch is genuine. But the claimed IMPACT ("watch to 95%, reopen, get seeked back to 95%") is impossible, because the finder never traced where `watchHistory` comes from.

`useHandleCurrentMediaContinuity` -> `useGetContinuityWatchHistoryItem` -> GET /v1/continuity/item/:id (routes.go:554) -> `HandleGetContinuityWatchHistoryItem` -> `Manager.GetWatchHistoryItem` (history.go:153) -> `getWatchHistory` (history.go:426). The authoritative gate lives there, at the SAME 90% threshold the finder claims is missing (history.go:445-455):

    ratio := ret.CurrentTime / ret.Duration
    if ratio >= IgnoreRatioThreshold {   // 0.9, history.go:17
        go func() { _ = m.fileCacher.Delete(...) }()   // purges the record
        return nil, false
    }
    if ratio < 0.05 { return nil, false }

So at 95% the server returns {Item: nil, Found: false} and deletes the record. The client then fails three independent ways before line 113's ratio term is ever evaluated meaningfully: video-core.tsx:1660 gates the whole restore branch on `watchHistory?.found` (false); continuity.hooks.ts:112 returns 0 on `!item`; and line 113 has no item to divide. manager_test.go:275 documents this deliberately: "Purge the resume item by reading it (ratio 0.99 >= 0.9 -> deleted, returns not-found)."

This also explains the asymmetry the finder read as evidence of a bug: getEpisodePercentageComplete consumes the BULK GetContinuityWatchHistory endpoint, which returns unfiltered items and therefore genuinely needs its own 90% check. getEpisodeContinuitySeekTo consumes the PER-ITEM endpoint, which is already server-filtered. Line 113 is redundant belt-and-suspenders that happens to be written wrong; observable behavior is correct via the server. Callers checked: only video-core.tsx:1661 (grep for getEpisodeContinuitySeekTo across seanime-web/src returns exactly one call site), and it has no compensating cutoff of its own — it doesn't need one.

---

## C. NAK-5 — found by the verifier, not a finder (recorded separately)

### NAK-5 — `sendSessionStateToClient` marshals the Participants map with no lock — **fixed** (high)

- **Location:** `internal/nakama/watch_party_host.go:500-508` (pre-fix)
- **What:** `sendSessionStateToClient` handed the whole `session` — including the
  `session.Participants` map — to `SendEvent`, which JSON-marshals it, while holding **no lock**. It is
  called from the peer-status/buffer-update handlers concurrently with map **deletes** at `:571`/`:605`
  that *do* hold `session.mu`.
- **Why it matters:** an unlocked map read racing a map write is a **fatal Go runtime throw**
  (`concurrent map iteration and map write`). Not a recoverable panic — it kills the whole server
  process. This is the genuinely dangerous remnant of the old F12 finding; the part the finder reported
  (NAK-3) was the *narrower* survivor.
- **How it surfaced:** the adversarial verifier assigned to refute NAK-3 confirmed a narrower race, then
  flagged this strictly-worse adjacent bug that no finder had reported. Worth noting as evidence the
  verify stage earns its cost — it is not just a rubber stamp.
- **Fix applied:** snapshot the session under `session.mu.RLock()` (shallow-copy the 7 exported fields,
  deep-copy the participants map) and release the lock **before** the network send.
- **Regression risk / how it was contained:** the snapshot must not drop wire fields. `WatchPartySession`
  has exactly 7 exported fields (`watch_party.go:146-159`) and all 7 are copied, so the JSON payload is
  byte-identical. `mu` is `json:"-"` and is deliberately left zero rather than copied.
  `WatchPartySessionParticipant` has no mutex, so the per-participant value copy is safe — confirmed by
  `go vet ./internal/nakama/...` passing clean.

---

## D. Fix ledger — 2026-07-16

Applied this session, `go build ./...` + `go vet` green:

| id | what | status |
|----|------|--------|
| AUTH-1 | `h.UserOnly` on `/torrent-client/download` (+ `action`, `get-files`, `rule-magnet`); list/details stay open | **fixed** |
| AUTH-2 | `RequireAdmin` inside `HandleTorrentClientAction` for `pause-all`/`resume-all`/`set-limits`/`move-storage`/`add-magnet`; added the missing `guardPrivilegedTorrentClient` strict-local boundary | **fixed** |
| AUTH-3 | `h.AdminOnly` on all auto-downloader mutations (rule/profile/run/simulation/item); GET reads left open | **fixed** |
| AUTH-4 | `guardPrivilegedExtensionManagement` on user-config POST; `h.AdminOnly` on the GET; `isValidExtensionIDString` validation moved into `SaveExtensionUserConfig` (the shared function) so the bucket key can't be an arbitrary path | **fixed** |
| NAK-5 | session snapshot under `RLock` before `SendEvent` (fatal map race) | **fixed** |
| NAK-3 | `session.mu` made the single owner of participant fields; lock order `wpm.mu -> session.mu` preserved | **fixed** |
| VET-3 | 6 transcoder `defer`+`time.Since` timings wrapped in closures | **fixed** |

Deliberate deviations from the finders' proposed fixes (each would have caused a regression):
- **AUTH-1:** did *not* put `IsAdmin` inside `guardPrivilegedTorrentClient` — it is shared with
  `HandleGetActiveTorrentList`, the high-frequency `/torrent-client/list` poll, and would have 403'd the
  torrent list for every non-admin whenever a custom executable path is configured.
- **AUTH-3:** used `AdminOnly`, not `UserOnly` — `UserOnly` only blocks networked-anon and would still let
  any registered `role="user"` account rewrite server-wide download rules, which is the actual finding.
- **AUTH-4 GET:** used `AdminOnly` (= `RequireAdmin`) rather than the full privileged guard, whose
  `IsStrict() && !isRequestFromTrustedLocal` clause would 403 a *remote admin* merely viewing the config —
  i.e. this fork's own Denshi-to-Pi deployment shape.
- **NAK-3:** did *not* add a per-participant mutex (the struct is JSON-marshaled; a `sync.Mutex` makes
  every value copy a vet failure) and did *not* make `checkAndManageBuffering` take `wpm.mu` (violates its
  `:698` contract and inverts `waitForPeersReady`'s `bufferMu -> session.mu` order = **deadlock**).
- **AUTH-2 path check:** scoped the `IsAbs` + strict-filesystem validation to `add-magnet` only, not
  `move-storage` — move-storage legitimately targets user-chosen dirs outside the strict roots, and
  validating it would break existing relocations.

---

## E. Pre-existing red tests on `main` (NOT caused by the audit fixes)

Verified by running the identical tests in a detached worktree at `HEAD` (`2d1a15aa`): these fail the same way
before any audit fix. Recorded because two of them assert real security invariants that are currently violated.

### PRE-1 — strict mode blocks hosted playback, contradicting an explicit test invariant — **open** (medium)

- **Location:** `internal/handlers/local_security_test.go:1022-1050`
  (`TestMediaConsumptionHandlersDoNotUseStrictLocalOnlyBoundary`)
- **What:** with `security.SetSecureMode(security.SecureModeStrict)` and a server password configured, the test
  asserts `HandleDirectstreamPlayLocalFile` and `HandleRequestMediastreamMediaContainer` fall through to normal
  body binding (**400** on a malformed body) for an authenticated *hosted* request
  (`Host: demo.example`, `RemoteAddr: 203.0.113.10`). Both currently return **403**.
- **Why it matters:** the test documents the intended invariant — *media consumption must not be gated by the
  strict local-only boundary* — and the code violates it. In strict mode, remote playback is blocked outright.
  The test name literally says this "should no longer short-circuit on the old strict local-only guard", so
  this is a regression against stated intent, not a stale test.
- **Blast radius / who is affected:** **strict mode only.** `SecureModeDefault` is `""` (`security.go:9-14`),
  so the Pi deployment at seanime.clinshaiju.dev is unaffected — this does not explain any current playback
  issue. It would bite anyone who turns strict mode on.
- **Status:** not fixed — out of scope for this pass, and fixing it means deciding whether strict mode is
  *meant* to allow remote playback. Flagged for a decision.

### PRE-2 — `usesPrivilegedCommandSettings` treats default executable paths as privileged — **open** (low)

- **Location:** `internal/handlers/local_security_test.go:1208-1209`
  (`TestUsesPrivilegedCommandSettings/ignores_default_executable_paths`)
- **What:** the subtest asserts the helper returns `false` for default executable paths; it returns `true`.
  (`detects_custom_paths_and_custom_args` passes.)
- **Why it matters:** over-broad privilege detection — a default-path setup is treated as privileged, so
  privileged guards engage where they should not. Consistent with the AUTH-1 analysis, where
  `isPrivilegedTorrentClient` gating the shared list path was identified as a regression risk.
- **Status:** not fixed.

### PRE-3 — `internal/local` + `internal/library/scanner` tests cannot run in this environment — **n/a**

- 4 failures in `internal/local` and 9 in `internal/library/scanner` (incl. `TestScanLogger`), all with:
  `missing AniList fixture for AnimeCollectionWithRelations (default); use an authenticated client and set
  SEANIME_TEST_RECORD_ANILIST_FIXTURES=true to refresh fixtures`.
- **Consequence for this pass — stated plainly:** the tests that would cover the **SYNC-1/SYNC-2 lock changes**
  and the **VET-1 ScanLogger field removal** are fixture-blocked and cannot run here. Those two fixes therefore
  have **no automated verification** — they rest on code review plus the regression workflow. Both need a manual
  check against a real AniList-authenticated instance.
- `internal/nakama` tests **pass** (`ok  seanime/internal/nakama`), so the NAK-3/NAK-5 lock changes do have a
  green suite behind them.

---

## F. Regression check of the fixes themselves — 2026-07-16

A second adversarial workflow (7 hunters over the applied diff -> Opus verifiers whose job was to *refute*)
asked only: **did these fixes break something that worked?** Result: **1 confirmed regression, 2 refuted.**

### R1 — our own PLG-4 fix silently killed provider `fetch()` after "Reload extensions" — **fixed** (high)

- **Location:** `internal/extension_repo/goja_base.go:276-281` (`gojaProviderBase.ClearInterrupt`)
- **What:** the PLG-4 leak fix added `g.fetches.closeAll()`, which sets `fetchRegistry.closed = true`
  **permanently**. But unlike `GojaPlugin.ClearInterrupt` (`goja_plugin.go:111-118`), the provider path never
  called `DeletePluginPool`. `Manager.GetOrCreatePrivatePool` returns an **existing** pool as-is and *discards*
  the new `initFn`, so after a reload the new provider inherits a stale pool whose factory still points at the
  **old, closed** registry. `fetchRegistry.add` then closes every Fetch on arrival → `beginRequest()` returns
  false → the promise never settles → **`fetch()` hangs forever with no error**.
- **Why it matters:** every external AnimeTorrent / Manga / Onlinestream / CustomSource provider loses
  `fetch()` — i.e. **torrent and manga search silently stop working** — after an admin clicks
  Settings → Extensions → "Reload extensions" (`HandleReloadExternalExtensions` →
  `interruptExternalGojaExtensionVMs`, which never deletes pools). Affects the **local password-less desktop
  install too** — nothing to do with the auth work.
- **Confirmed regression, not pre-existing:** at HEAD, `ClearInterrupt` was only `store.Stop()` +
  `scheduler.Stop()`, and `BindFetch`'s return value was discarded. Stale-pool reuse leaked goroutines but
  `fetch()` **kept working**. The new registry is precisely what turned a leak into a functional break.
- **Verifier's corrections to the hunter:** it breaks on the **first** bulk reload (not the second — startup is
  safe because `LoadOnlyWrapper` gives the two boot calls disjoint type sets, so no `ClearInterrupt` fires);
  and it is recoverable per-extension via a single-extension reload (which does call `DeletePluginPool`),
  not only by a process restart. Severity corrected critical → high.
- **Fix applied:** mirror `GojaPlugin.ClearInterrupt` — `DeletePluginPool(g.ext.ID)` **before**
  `g.fetches.closeAll()`, nil-guarding `runtimeManager`. Ordering matters and is documented at both sites.
  This also fixes the pre-existing latent bug where a stale pool handed reloaded providers VMs bound to the
  old provider's stopped store/scheduler.

### Refuted (recorded so they are not re-chased)

- **R2 `<goja-fetch>` — "Close()'s drain goroutine can leak forever on a timeout-less fetch."** Refuted: the
  trigger is unreachable. It relied on `timeout: 0` from JS reaching `SetTimeout(0)`, but the option parser
  (`fetch.go:356+`) never lets that through.
- **R1 `<client-compat>` — "torrent-client page exposes now-admin-only bulk actions, causing silent 403s."**
  Refuted **as a regression** (it is the gate's intended server-side effect), but the code facts were correct
  and it revealed a real *completeness* gap in the chosen "full frontend gating" scope. **Acted on anyway** —
  see below.

### Follow-on UI gating (not a regression; completing the chosen scope) — **fixed**

`seanime-web/src/app/(main)/torrent-client/page.tsx` had no role check while its actions had just become
admin-only server-side. Now gated on the existing `useIsAdmin()`: the Add-torrent / Pause-all / Resume-all
header block, the global speed-limits popover (`set-limits`), and **both** `move-storage` entry points (the
toolbar button and the per-torrent context-menu item). Per-torrent pause/resume/remove remain available to any
logged-in user, matching the server, where only the server-wide actions carry `RequireAdmin`.

Also closed: `autodownloader-rule-item.tsx` gated the row click + chevron on `useIsAdmin()` — one guard in the
shared component covers both the admin-gated page and the per-anime rule list, where non-admins can still
*read* rules (the GET stays open) but no longer get an edit affordance that would 403.

### Verified NOT regressed (the important non-findings)

- **Local password-less desktop install is untouched.** `Config.Server.Password == ""` →
  `IdentityMiddleware` injects the admin (`identity.go:44-49`), and `useIsAdmin()` returns
  `!serverHasPassword || role === "admin"` → true. Every gate is a no-op there. This is upstream seanime's
  primary shape and the single most important non-regression.
- **The auto-downloader engine never goes through HTTP** — it calls
  `ad.torrentClientRepository.AddMagnets(...)` directly (`autodownloader.go:1427`), so admin-gating the
  routes does not stop automatic downloading for anyone.
- **nakama does not proxy any gated route** — it only forwards `/api/v1/nakama/host/*`.
- **`internal/nakama` tests pass**, covering the NAK-3/NAK-5 lock changes.

---

## G. Round 2 — the remaining findings — 2026-07-16

Round 1 fixed the security + crash tier (11/24). Round 2 covered the rest. Static after both rounds:
`GO BUILD OK` / `WEB TYPECHECK OK`, and **`go vet ./internal/...` is now completely silent** (it emitted 8
diagnostics at session start).

| id | what | status |
|----|------|--------|
| DBG-1 | `playPreloadedStream` now passes `ExpectedSize`, so commit 4ebfa601's truncated-CDN guard actually fires on the preload/prewarm path (it was inert there — the common path in this fork) | **fixed** |
| EVT-2 | `AddConn` returns the `*WSConn`; `RemoveConn` matches by pointer identity, killing ghost connections | **fixed** |
| EVT-1 | `connState()` locked snapshot replaces the unlocked `m.Conns`/`hasHadConnection` read | **fixed** |
| MC-3 | videocore effects + insight subscriber goroutines select on `dispatcherStop` | **fixed** |
| MC-2 | mediacore `SetupSharedEffects` selects on the existing `stopCh` | **fixed** |
| DB-1 | `CacheLayer.Close()` (+ `stopOnce`) stops the queued-update-sync ticker; wired into session shutdown | **fixed** |
| TS-1 | `currentTorrent`/`currentFile` writes guarded as an atomic pair (`stream.go`, and `Shutdown()` — a second unlocked writer the finder missed) | **fixed** |
| PLAYLIST-1 | `sessionGen` guard + locked/unlocked `stopPlaylist` split; a stray unconditional `m.mu.Unlock()` deleted | **fixed** |
| MEDI-NAK-2/3/4 | manga: concurrency cap on `getPageDimensions`, race-free per-goroutine error, fd closed per page | **fixed** |
| DB-3 | `accountCacheMu` guards the package-level `accountCache`; no lock held across a DB call | **fixed** |
| DB-4 | `TrimMediastreamVideoFiles` only wipes the in-memory store when trimming actually happened | **fixed** |
| MERGE-1 | Denshi seek-bar highlight now uses the same `{guardIntro:false, heuristics:true, duration}` predicate as the skip logic | **fixed** |
| PLG-1 | goja data race — **NOT fixed, deliberately** (see below) | **deferred** |
| PLG-2 | plugin execution timeout — **NOT fixed** (entangled with PLG-1's rework and with `DeletePluginPool`, which the R1 fix newly calls) | **deferred** |

### Deviations from the verifiers' prescribed fixes (each avoided a regression)

- **PLAYLIST-1:** the verifier's "give `StopPlaylist` a thin wrapper that takes `m.mu`" **self-deadlocks** — it
  missed that `playEpisode` calls `StopPlaylist` on three error paths (`:610/:627/:640`) and is itself only ever
  called with `m.mu` held. Split into `stopPlaylistLocked` (locked callers) + `StopPlaylist` (unlocked callers).
  The verifier's risk #4 was real: `UnsubscribeFromPlaybackStatus` closes a *shared* `"playlist-manager"` key,
  so a superseded goroutine would deterministically close the NEW session's channel — hence `sessionGen`.
- **MC-2/MC-3/DB-1:** did **not** mirror `mpvcore`'s Range-close idiom — it would widen `dispatch()`'s TOCTOU
  into a `send on closed channel` panic, trading a bounded leak for a process crash. Selected on the existing
  stop channels instead.
- **EVT-2:** applied only the identity-removal half. Same-ID eviction was **rejected** (would kill a live tab
  when a second opens → reconnect flap) and read deadlines were **rejected** (no server ping ticker exists →
  would drop every idle client). Both documented in code comments.
- **DB-1:** the verifier's regression risk #3 (custom sources breaking) was **refuted** with evidence — the
  manager subscribes under a per-session uuid and `Close()` is once-guarded; the fix is net-positive because
  that goroutine was leaking on eviction too.
- **TS-1:** critical section deliberately stops before `ResetBaselines()`/`cleanupActiveTorrentFiles()` — both
  take `c.mu`, which is non-reentrant.

### PLG-1 — confirmed real, deliberately NOT fixed

Reproduced live under `-race` (`goja.(*Promise).fulfill` at `fetch.go:237` racing `Promise.State()`). Both
candidate fixes are worse than the bug: `$await` is public plugin API (`core.d.ts:91`) and `WaitForPromise` is
a 10ms poll-sleep loop that **only works because** the pump resolves from a separate goroutine — the
cross-goroutine resolve is load-bearing, not an oversight. Routing through a scheduler deadlocks `$await`;
removing the binding is a plugin-facing API break. Correct fix = replace the poll loop with a real event loop
(e.g. `goja_nodejs/eventloop`) so each VM has one owner goroutine. That touches every provider and hook —
its own pass. The safe tail WAS landed: `BindFetch`'s `defer/recover` was inside the `for` loop, so it fired
only at goroutine exit — one panicking callback killed the pump and hung every later fetch on that VM.

### Verification method

Every "pre-existing failure" claim in this pass was proved by running the identical tests in a **detached
worktree at HEAD** and diffing the failure set — not asserted. The leak fixes were proved by writing goroutine
tests with negative controls and then **falsifying them**: reverting the ticker fix made the test report
`LEAK: shared_platform.(*CacheLayer).startQueuedUpdateSync still running after Close()`; restoring it → ok.

### INCIDENT — tree wipe and recovery (process lesson)

Two subagents independently ran `git stash` on the shared working tree. `git stash` resets to HEAD, silently
discarding **all** uncommitted work (reflog: `reset: moving to HEAD`); each agent then recovered only its own
files, and one dropped the stash. **23 files** of round-1 work were lost — all auth gates, nakama locks,
`sync.go`, goja, every web change, plus the user's own uncommitted `main.go --version`.

Recovered in full: dropped stashes survive as unreachable commits (`git fsck --unreachable`), and
`0f1448663` held the complete pre-wipe state. Files were classified lost-vs-live and only the 23 casualties
restored worktree-only, leaving live round-2 edits untouched. All fix markers re-verified individually.

**Root cause: the agent prompts forbade committing but not `stash`/`reset`.** Fix for next time: run
concurrent fix agents with `isolation: "worktree"`, and explicitly ban destructive git verbs. Note the
user's `CHANGELOG.md` was never at risk — it was committed by the user in `2d1a15aa`, not destroyed.

---

## H. Round-2 regression check — result

Second adversarial workflow (5 hunters over the round-2 diff, deadlock/premature-teardown prioritised):
**0 claimed regressions.** All five returned well-formed `regressions: []` for their area — genuine clean
verdicts, not errors (verified against the run journal; 418k subagent tokens, 146 tool calls).

**Honest weight of this result:** zero claims means the Opus verify stage never ran, so this is
"five adversarial hunters found nothing", NOT "verifiers confirmed clean" — weaker evidence than round 1's
1-confirmed/2-refuted. It carries weight only because the same harness caught R1 (our own PLG-4 fix silently
killing provider `fetch()`) one round earlier, so it has demonstrated detection power.

### Final gate — 2026-07-16

```
scripts/check.sh          GO BUILD OK / WEB TYPECHECK OK
go vet ./internal/...     silent (8 diagnostics at session start -> 0)
go test                   ok: nakama, events, mediacore, videocore, core, util/filecache,
                              platforms/shared_platform
```
All other suites fail for **pre-existing** reasons, each proved by running the identical tests in a detached
worktree at HEAD and diffing the failure set:
- `internal/local`, `internal/library/scanner` — `missing AniList fixture for AnimeCollectionWithRelations`
- `internal/manga`, `internal/database`, `internal/debrid`, `internal/torrentstream`, `internal/playlist` —
  Windows `t.TempDir()` teardown: `unlinkat ... process cannot access the file` (sqlite handle)
- `internal/extension_repo` — 2 pre-existing goja DX failures
- `internal/handlers` — PRE-1/PRE-2 (see §E)

### Coverage gaps — stated, not buried

- **SYNC-1/SYNC-2 and VET-1 have NO automated coverage.** The `internal/local` + `scanner` suites are
  fixture-blocked, so the `sync.go` lock restructuring and the `ScanLogger` field removal rest on code review
  and the regression workflow alone. Manual test required against an AniList-authenticated instance.
- **`-race` could not run on the lock fixes** in `torrentstream`/`playlist` (needs cgo; unavailable on this
  Windows host). Those races are reasoned about, not detector-proven. The goja PLG-1 race and the EVT-1 race
  WERE detector-proven (the latter falsified by reinstating the old unlocked read and observing the warning).

### Two findings raised but deliberately left open (out of the audit's scope, flagged for a later pass)

- `internal/playlist/manager.go:243` — `m.clientId` written unlocked while read under lock.
- `internal/playlist/manager.go:376` — `mediacoreSubscriber.Events()` will nil-deref if `mediacoreCoordinator`
  is nil.

### Final status: 23 of 24 confirmed findings fixed. Nothing committed, nothing deployed.

---

## I. DYNAMIC verification — and the bug the entire static audit missed

The static audit (47 agents), two adversarial regression workflows, and `go vet` **all missed this**. It was
found within minutes of actually booting the server and sending real HTTP requests.

### AUTH-5 — `UserOnly` returned 403 **and ran the handler anyway** — **fixed** (high)

- **Location:** `internal/handlers/identity.go` (`guardStreamingUser`), via
  `RespondWithStatusError` (`routes.go:678-680`).
- **What:** `RespondWithStatusError` is `return c.JSON(...)`, and `c.JSON` returns **nil** on a successful
  write. `guardStreamingUser` wrote the 403 and returned that nil. Every caller uses
  `if err := h.guardStreamingUser(c); err != nil { return err }` — so `err` was nil, the guard fell through,
  and **`next(c)` / the handler body executed regardless**. The response said 403 while the action was
  performed.
- **Proof (observed, not reasoned):** the 403 responses carried **TWO** JSON bodies — the denial, then a real
  error from inside the handler:
  ```
  anon GET /debrid/torrents        -> 403 {"error":"streaming requires logging in"}
                                          {"error":"debrid: Provider not set"}      <- handler ran
  anon POST /playback-manager/play -> 403 {"error":"streaming requires logging in"}
                                          {"error":"code=400, message=json:..."}    <- reached c.Bind
  ```
  Control: `AdminOnly` emits exactly ONE body (`RequireAdmin` returns a real non-nil error, so it returns the
  write result and never calls `next`) — so AdminOnly was always sound, and AUTH-2/3/4 are genuinely blocked.
- **Why it matters:** **AUTH-1's fix was purely cosmetic.** Adding `h.UserOnly` to the torrent-client routes
  changed the status code and nothing else — anon could still drive torrent/debrid/playback work. Worse, this
  is **pre-existing**: the debrid routes have carried `h.UserOnly` since before this audit, so *those* gates
  were never enforcing either.
- **Blast radius:** all 15 `h.UserOnly` routes (`routes.go:323-326, 518-522, 597-603`) plus 10 direct
  `guardStreamingUser` call sites (`playback_manager.go:17,57,219`, `directstream.go:25`, `debrid.go:438,567`,
  `mediastream.go:78`, `torrentstream.go:130`, `nakama_rooms.go:227`).
- **Fix applied:** `guardStreamingUser` now calls `respondWithAbort` (`local_security.go:31-41`), which writes
  the JSON and returns the `errGuardResponseWritten` sentinel — the pattern the codebase already used
  correctly everywhere else. One change in the shared function repairs the middleware and all 10 call sites.
- **Regression risk:** these routes were *effectively ungated*, so the fix is the first time they actually
  block. The password-less desktop case must stay unaffected (`IdentityMiddleware` injects the admin →
  `dataUserID != 0` → guard passes) — under dynamic re-verification.

### The dynamically-verified auth matrix (all 14 cells observed)

Token correctness proved first (`X-Seanime-Token` = `util.HashSHA256Hex(password)`, `core/app.go:243`;
no token → 401, wrong token → 401, correct → 200) so the matrix is non-vacuous. Principals confirmed via
`/user/me`: admin (id 1, role=admin), bob (id 2, role=user). Isolation via the real `--datadir`/`--port` flags
(`core/flags.go:54-56`) — the user's real data was never touched.

All 14 cells matched expectation. Non-403 results are genuine handler-reach, confirmed by body
(`"destination is required"`, `"debrid: Provider not set"`). Denials read `"admin privileges required"` /
`"streaming requires logging in"`.

**AUTH-4 id validation — PASS, non-vacuous:** control `goodext` → 200 and wrote
`datadir/cache/ext_user_config_goodext.cache` (so the validator isn't rejecting everything); `../../evil` and
`..\..\evilwin` → `{"error":"invalid extension id"}`, and a filesystem sweep for `*evil*` found nothing.

**Password-less no-op — PASS**, with one honest caveat: `POST /extensions/user-config` returns 403 when curl
sends no `Origin`/`Referer` — a **curl artifact, not a regression**. With `Origin` set (what a browser/Denshi
sends) → 200. The untouched, pre-existing `/extensions/external/install` behaves identically, so the fix
aligned user-config with the trust model install/uninstall already used. Narrow real caveat: header-less
scripted clients that previously could POST user-config on a password-less install now get 403.

### Method note

`-race` **does work** on this host (gcc at mingw64) — an earlier agent claimed otherwise and the audit
initially recorded that as an unclosable gap. It was wrong; §H's "-race unavailable" caveat is retracted and
the lock fixes are under race-detector verification.

---

## J. Runtime probes — results, and two more bugs static review could not see

Three servers booted on isolated datadirs/ports (`--datadir`/`--port`, `core/flags.go:54-56`); the user's real
data was never touched. Every probe was required to prove **detection power** — that it can actually fail.

### Verified PASS, detection-power proven (the strongest evidence in this audit)

| probe | covers | observed |
|---|---|---|
| R1 in-process | R1 | 5 bulk reloads → `fetch()` resolved every time (body len 23 = the real served body) |
| **R1 detection power** | R1 | The SAME probe against the reproduced **pre-fix** `ClearInterrupt` → `HANG: fetch() promise never settled within 8s`. Fixed → PASS. **This proves R1 was a real, silent, reproducible functional break and that the fix closes it.** |
| R1 over HTTP | R1 | 60 per-extension reloads, 30 post-reload `fetch()` calls, all resolved <200ms, zero timeouts |
| PLG-4 | PLG-4 | fetch pump goroutines **37 → 36** across 60 reloads (flat) |
| DB-1 | DB-1 | `startQueuedUpdateSync` ticker returns to baseline across 8 session builds + 32 evictions |
| EVT-2 key | EVT-2 | Survivor stays hooked when a same-id sibling closes. **Old code proven to evict the LIVE tab:** `OLD: tab 1 got NO pong despite an open socket` |
| EVT-2 churn | EVT-2 | 12 same-id conns, 8 randomized closes → 4 survivors, all still receiving. Zero ghosts |
| EVT-2 reconnect | client-identity-restart-bug | Reconnect registered under the **correct** id, events flow |
| EVT-1 | EVT-1 | `-race` **FIRED** on the pre-fix pattern (`AddConn() websocket.go:208` vs `oldConnState()`); clean on the fix |
| EVT-1 sidecar | EVT-1 | Server survived 20s idle, 35s churn, and all-closed — no spurious `os.Exit` |
| Manga fd leak | MEDI-NAK-4 | 50 loads × 150 pages, handles **+0**. Detection proven: holding refs → **+1500** (exactly linear); dropping → peak +468 |
| Manga cap | MEDI-NAK-2 | Peak concurrency **5** (fixed) vs **60** (unbounded control) |
| Manga err race | MEDI-NAK-3 | Failures no longer poison sibling pages; 54/80 dims returned, `err == nil` |
| Manga `-race` | MEDI-NAK-3 | Zero `WARNING: DATA RACE` across the whole manga tree |

### NOT_RUN — stated honestly

**MC-2 and MC-3 could not be exercised.** Those modules are built **lazily, per session, only on real playback**.
Across 8 user sessions and 32 evictions the profile showed exactly ONE
`mediacore.(*Coordinator).SetupSharedEffects.func1.1` and ONE `videocore.(*VideoCore).setupOnlinestreamEffects.func1`
— the App-global admin instances, not per-user ones. Driving a per-user videoCore needs real playback
(mpv/debrid/torrentstream), which cannot be triggered headlessly. **These two fixes remain unverified at
runtime** and rest on code review + the falsified unit leak tests. Manual test required.

### CHROME-1 — chromedp pump goroutine leak — **open** (medium, pre-existing)

- **Location:** `internal/goja/goja_bindings/chromedp.go:109-130`; return discarded at
  `internal/extension_repo/goja.go:77`.
- **What:** `go func() { for fn := range c.ResponseChannel() { ... } }()` — the pump is never stopped and the
  channel never closed when the VM/pool is disposed. Same defect class PLG-4 fixed for `BindFetch`, on the
  sibling binding, missed because the audit only looked at fetch.
- **Measured, not theorised:** `BindChromeDPWithScheduler.func1` **37 → 427** over 60 reloads (+390, ~6.5 per
  reload), never reclaimed after forced GC. Total process goroutines **136 → 612**.
- **Why this is strong evidence:** the same pprof snapshot contains a natural control — `BindFetch.func1` stayed
  **flat (37 → 36)** over the identical window because it was already fixed. Same VM churn, same measurement:
  the fixed pump flat, the unfixed one climbing. It also retroactively proves the PLG-4 fix works.
- **Pre-existing, not a regression:** `git diff --stat HEAD -- internal/goja/goja_bindings/chromedp.go` is empty.
- **Status:** fix in progress, mirroring the `fetchRegistry` + `DeletePluginPool`-before-`closeAll` pattern —
  explicitly avoiding the R1 shape (closing the registry without dropping the pool = every call hangs forever).

### ROUTE-1 — bulk "Reload extensions" was unreachable — **fixed** (medium, pre-existing)

- **Location:** `internal/handlers/routes.go:536-537` (`529-530` at HEAD).
- **What:** BOTH handlers were registered on the same method+path:
  ```go
  v1Extensions.POST("/external/reload", h.HandleReloadExternalExtensions) // bulk
  v1Extensions.POST("/external/reload", h.HandleReloadExternalExtension)  // single
  ```
  Echo keeps only the last, so the **bulk handler was dead code**. Both client hooks
  (`endpoints.ts:841-850`) target that same path — so clicking "Reload all extensions" hit the single-extension
  handler with an empty id and **silently reloaded nothing**.
- **Fix applied:** register once; `HandleReloadExternalExtension` dispatches to
  `HandleReloadExternalExtensions` when the request carries no id. A bind failure is treated as "no id" (the
  bulk client sends no body). No API/codegen change, both behaviours reachable, no dead code.
- **Note:** this means the R1 blast radius was narrower than believed over HTTP — but R1 was still proven real
  in-process, and the *single*-extension reload path (which the UI does reach) runs the same `ClearInterrupt`.

---

## K. Race-detector verification — the "no coverage" gaps, closed

`-race` **works on this host** (gcc at mingw64). §E/§H's "`-race` unavailable" caveat came from one agent's
incorrect claim and is **retracted**. Re-running properly changed the picture substantially.

### All six lock fixes: CLEAN + FALSIFIED

Each fix was reverted in place, the detector re-run to confirm it **fires**, then restored (md5-verified
identical). A test that cannot fail proves nothing; these can fail.

| target | falsification result (the proof) |
|---|---|
| **SYNC-1** | reverting to `return q.queueState` → `WARNING: DATA RACE` in `json.Marshal`→`reflect.maplen` vs `mapassign_fast64` — **exactly the `handlers/local.go:160` HTTP-handler scenario** |
| **SYNC-2** | single unlocked writer → race against the **real** `checkAndUpdateLocalCollections` read (`sync.go:153`) |
| **PLAYLIST-1** | removing `StopPlaylist`'s lock → production-vs-production races via `resetPlaylistLocked`/`stopPlaylistLocked`/`sendCurrentPlaylistToClient`. **No deadlock** (the strongest result — real Manager, real entry points) |
| **NAK-5** | reverting to `SendEvent(…, session)` → `reflect.maplen` vs `mapassign_faststr` — the fatal concurrent-map-iteration scenario |
| **TS-1** | clean, but **both sides transcribed** — see the caveat below |
| **DB-3** | unlocking both accessors → race on the package-level cache (real accessors) |

**This retires the audit's biggest stated gap.** SYNC-1/SYNC-2 previously had *zero* automated coverage
(fixture-blocked suites); they are now falsification-proven at the race-detector level.

**Weakest result, stated plainly: TS-1.** Both writer and reader had to be transcribed stand-ins (driving
production needs a live torrent client), so its falsification proves the lock discipline on the real fields but
does **not** drive production's `StartStream`. SYNC-1, SYNC-2, PLAYLIST-1, NAK-5 and DB-3 all falsified against
genuine production functions.

### Five MORE real races found — three are gaps in THIS AUDIT's own fixes

| # | where | verdict |
|---|---|---|
| 1 | `torrentstream/client.go:165` — status goroutine reads `c.currentFile.IsPresent()` **without `c.mu`**, racing TS-1's newly-locked write | **TS-1's fix is INCOMPLETE** — fix in progress |
| 2 | `playlist/manager.go` — `markCurrentAsCompletedLocked:567` writes `IsCompleted` under `m.mu`; the goroutine spawned at `stopPlaylistLocked:773` reads it **unlocked** at `:783`. **~75% reproducible** | **PLAYLIST-1 does not cover it** — fix in progress |
| 3 | `watch_party_host.go:87` — still passes the raw `session` to `SendEvent` unlocked, the same pattern NAK-5 fixed at `:507` | **NAK-5 missed a second site** — fix in progress |
| 4 | `shared_platform/cachelayer.go` — `lastCollectionUpdate` write/write (`:701` vs `:607` in a goroutine spawned at `:844`); bare `time.Time`, no mutex | pre-existing, unguarded at HEAD — fix in progress |
| 5 | `playbackmanager/playback_manager.go:630` — `Cancel()` reads `pm.MediaPlayerRepository` unguarded vs the `:285` write under `pm.mu` | pre-existing, file untouched by the audit — fix in progress |

**The lesson: a fix that passes review can still be incomplete, and only the detector says so.** Three of our
own fixes locked one side of a race and missed the other. Static review — including two adversarial regression
workflows — confirmed all three as complete.

### Sweep across 14 package trees — 267 races, **none a regression from this audit**

- **Clean:** events, nakama, mediacore, core, filecache, database/models, manga/providers, extension_repo/prompt.
- **~240 of 267** live in `goja_bindings/fetch.go` + `util/goja/scheduler.go` + `plugin/ui` — this is **PLG-1's
  known architecture** ("goja Runtime is not goroutine-safe"). The racing files are untouched by the audit and
  the VM-mutating pump goroutine exists verbatim at HEAD. This independently corroborates PLG-1's severity and
  why it needs the event-loop rework rather than a patch.
- **videocore (12) + playlist (sweep-side):** TEST-HARNESS races — `recordingWSEventManager.SendEventTo` appends
  to `m.sent` with no mutex, and `manager_test.go:216` reads manager fields unlocked. Production side is
  correctly locked. Worth fixing eventually: they **mask real races** in those suites.

### CHROME-1 — **fixed**, and proven by two independent methods agreeing

`internal/goja/goja_bindings/chromedp.go` got fetch's disposal shape: `done` channel, `closed atomic.Bool`,
`inflight` WaitGroup, `begin()`/`finish()` gates on 19 op goroutines, `send()` for the 2 listener callbacks, and
a `Close()` that cancels browsers → drains in-flight → closes `done`. Wiring: `goja.go` `ShareBinds` now returns
the handle instead of discarding it; `fetchRegistry` generalized to a `bindingRegistry` of closers; all three
plugin sites wired. Both VM lifecycles ride the **existing** `DeletePluginPool`-before-`closeAll` ordering — no
parallel teardown mechanism.

| probe | result |
|---|---|
| provider reloads, fixed | `[5 5 5 5 5 5 5 5]` — flat |
| same test, **pre-fix wiring** | `[10 15 20 25 30 35 40 45]` → **FAIL** (+5/reload, linear) |
| counter detection power | 10 discarded handles → +10; after `Close()` → 0 |
| plugin path | loaded `[7 7 7 7 7]` → unloaded `[0 0 0 0 0]` |
| R1 not reintroduced | `fetch()` resolved after 5 reloads; ChromeDP settled (no hang) |
| `-race` | 0 races across 40 binds + 40 closes |

**Two independent methods agree:** plugins bind 7 (loader + pool5 + uiVM), providers 5 → a mixed install
averages ~6.5/reload, which reproduces the live server's pprof measurement of **+390 over 60 reloads (~6.5/reload)**
exactly. The unit test and the running server arrived at the same number by different routes.

**Deliberate design deviation (correct):** `responseCh` is NOT closed, unlike fetch's channel.
`ListenBrowser`/`ListenTarget` give chromedp's *own* event loop a callback that sends on it — untracked and not
endable by a drain — so closing it would race them into a **send-on-closed panic inside chromedp's goroutine**,
where the sent closure's `recover` (inside the callback, not around the send) would not catch it. Instead `done`
ends the pump, the drain preserves promises, and `send()` stops listeners panicking or blocking.

**Bonus latent bug fixed:** chromedp's `else`-branch `defer recover` was **inside** the pump loop, so it only ran
at goroutine exit — one panicking response killed the pump and hung every later call. Fetch had this same bug
fixed during PLG-4; chromedp never did.

**Honest caveats:** (1) this does NOT fix PLG-1 for chromedp — the pump still calls `fn()` on its own goroutine
when `scheduler == nil`, identical to HEAD; that is PLG-1's deferred architecture, unchanged. (2) A residual edge
case is deliberately left alone: `NewBrowser` acquires `chromeSem` but only JS `browser.close()` releases it, so
an extension that leaks all 5 slots *and* has a 6th `newBrowser` blocked at the instant of unload would stall the
drain. Releasing the slot in `Close()` would race `Browser.Close`'s own receive and could block worse. Needs a
browser-leaking extension plus precise timing, and the outcome is no worse than today's baseline (where the pump
leaks 100% of the time regardless).

---

## L. The five races closed — and a methodology warning worth more than the fixes

| # | file:line | fix |
|---|---|---|
| 1 | `torrentstream/client.go:170-172` | **TS-1 gap** — snapshot `currentFile.IsPresent()` under `c.mu`, release before use |
| 2 | `playlist/manager.go:771-793` | **PLAYLIST-1 gap** — evaluate the all-completed predicate under the caller's `m.mu`; the goroutine now receives only `bool` + `dbId` |
| 3 | `nakama/watch_party_host.go:79-90, 501-507, 530-556` | **NAK-5 2nd site** — extracted the inline `:507` snapshot into `(*WatchPartySession).snapshot()`, applied at `CreateWatchParty` **and `broadcastSessionStateToPeers`** |
| 4 | `shared_platform/cachelayer.go:111,476,482,612,633,654,706` | `lastCollectionUpdate` → `atomic.Int64` (unix nanos, 0 = never) |
| 5 | `library/playbackmanager/playback_manager.go:630-641` | `Cancel()` snapshots the repo pointer under `pm.mu`, releases before the blocking `Stop()` |

Strategy throughout: **snapshot-under-lock-then-release**. No lock held across a network send, player call, or DB
call; no new mutex; no locked path calling a public wrapper; nakama's `wpm.mu → session.mu` order preserved; no
mutex added to any JSON-marshaled struct (`go vet` clean).

Each fix falsified — reverted, detector fired, restored md5-identical:
- **TS-1** → `Read at client.go:173` in the *real* `initializeClient.func2` status goroutine vs the locked write
- **PLAYLIST-1** → `Write at manager.go:567` (`IsCompleted = true`) vs `Previous read at manager.go:791`
- **NAK-5** → marshal vs locked map delete/insert
- **cachelayer** → `Write at :706` vs `Previous write at :612`
- **playbackmanager** → `Write at :286` vs `Previous read at :634`

### My premise was wrong about NAK-5's second site

I pointed the agent at `watch_party_host.go:87`. It proved that site was **already safe**: `CreateWatchParty`
holds `wpm.mu`, and every `session.Participants` writer also holds `wpm.mu`, so it was excluded. The real hole is
**`broadcastSessionStateToPeers:492`** — spawned via `go` from `:582`/`:645`, running **without** `wpm.mu`,
concurrently with `delete(session.Participants, peerID)`. That is the fatal
`concurrent map iteration and map write`. Both are now snapshotted.

### ⚠ A tight-loop writer silently defeats the race detector

The first TS-1 falsification reported **no race with the fix reverted**. Rather than bank that clean result, the
agent instrumented production: reads executed **6**, writes executed **11,099,065** — and the detector reported
nothing. Throttling the writer to ~1,400 writes made it fire deterministically.

> **A "clean" `-race` run against an unthrottled hot-loop writer is worthless evidence.** The detector's shadow
> state gets overwritten faster than the reader can be observed against it, so the race silently disappears.

This is a false negative that reads exactly like proof. Every probe in this audit was throttled for that reason.
Any future `-race` verification here must be, too.

### Adjacent races found, deliberately NOT fixed (documented, not dropped)

- `playlist/manager.go:572` — `markCurrentAsCompletedLocked`'s own goroutine passes the live playlist to
  `db_bridge.UpdatePlaylist`, marshaling every `Episode.IsCompleted` unlocked. Same class; a correct fix needs a
  deep copy of `Playlist`→`Episode`→`BaseAnime` — riskier than the race.
- `playbackmanager:275/282` — `pm.cancel` read/written unlocked in a spawned goroutine (two concurrent
  `SetMediaPlayerRepository` calls race). Induced at 150×; production calls it rarely.
- `playbackmanager:587-602` — `Pause`/`Resume`/`SeekTo`/`PullStatus` share the same unguarded field as `Cancel`.
  A shared accessor would fix all four, but proving no caller holds `pm.mu` (self-deadlock) exceeded scope.
- `torrentstream/client.go:244-247` — `torrentClient`/`timeSinceLoggedSeeding` read unlocked in the status loop.
- `torrentstream/playback.go` — `playback.currentVideoDuration` unguarded.

### Test-harness races that MASK real ones (worth fixing separately)

`testmocks/fake_platform.go:187` (`updateEntryProgressCalls` unlocked append),
`recordingWSEventManager.SendEventTo` (unlocked append to `m.sent`), `manager_test.go:216` (unlocked field reads).
Production side is correctly locked in each case — but these fire first and **hide real races** in the videocore
and playlist suites.

### Note on reading `go test` output here

`internal/playlist` reports **9 `--- FAIL` lines with 0 assertion failures and 0 panics** — all 10 are
`TempDir RemoveAll cleanup: unlinkat ... The process cannot access the file` (a sqlite handle still open at
Windows teardown). Grepping `--- FAIL` is therefore misleading in this repo; grep `Error Trace:` / `panic:`
instead. The same artifact accounts for the failures in manga, database, debrid and torrentstream.
