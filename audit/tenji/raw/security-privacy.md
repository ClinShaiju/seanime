# Tenji security/privacy crosscut audit — raw notes

Date: 2026-07-10
Scope (per assignment): server password/token storage (SecureStore vs AsyncStorage vs plain),
credentials/tokens in logs (expo-offline-logger sinks + console.log), http fallback / TLS handling,
OTA update integrity checks, deep-link parameter injection, secrets committed in app.config.ts/eas.json.

Repo: H:/Projects/seanime-tenji (iOS-only shipped target). Cross-checked server semantics only
against H:/Projects/seanime (not audited itself).

Pre-check: read H:/Projects/seanime/tenji-audit.md ledger (T1-T13, all fixed) — none of those
findings are security/token-storage related, so no overlap risk there.

## Files read

- src/atoms/storage.ts (MMKV-backed jotai storage adapter, plain string get/set/JSON helpers)
- src/atoms/server.atoms.ts (serverUrlAtom, serverAuthTokenAtom, sessionTokenAtom, serverStatusAtom)
- src/api/client/server-auth.ts (HMACAuth, hashServerPassword, getServerAuthHeaders, HMAC query-param helpers)
- src/api/client/requests.ts (buildSeaQuery, useServerMutation/useServerQuery, timeout handling)
- src/api/client/client-identity.ts (clientId/clientIdProof persistence + header/query helpers)
- src/api/components/websocket-provider.tsx (full)
- src/api/components/server-data-wrapper.tsx (skim — auth gating flow)
- src/lib/offline-logger.ts (full — persistEntry, redactSensitiveText, sanitizeEntryForExport, patchConsole)
- src/lib/utils/logger.ts (full)
- src/lib/ota/updates.ts (full — expo-updates wiring, no custom signature/integrity code, relies on expo-updates defaults)
- app.config.ts (full)
- eas.json (full)
- app/(out)/set-server-url.tsx (full — login/connect flow, http/https validation)
- src/api/client/server-url.ts (full — getServerBaseUrl, dev/prod URL selection)
- src/lib/player/external-players.ts (full — external player URL handoff via Linking.openURL)
- src/lib/downloads/download-manager.ts (relevant slice — fileUrl w/ HMAC token, not persisted)
- src/lib/player/local-file-source.ts (full — local-file HMAC-token stream URL construction)
- app/_layout.tsx (full — no incoming deep-link/Linking listener registered; expo-router default file-based scheme only)
- modules/expo-offline-logger/* (file listing; native sinks are append/read/clear/clipboard only, no network upload)
- grep sweep across src/** app/** for: SecureStore/AsyncStorage/mmkv, console.*, token/password/secret/auth,
  appendServerHMACToken/getServerHMACToken call sites, http:// literals, Linking/WebView usage,
  useLocalSearchParams (deep-link param consumers)

## Findings kept (see StructuredOutput)

1. **HIGH** — `src/api/components/websocket-provider.tsx:71` logs the full outbound
   `wss://.../events?...&token=<serverAuthToken>&session=<sessionToken>&proof=<clientIdProof>` URL via
   `logger("websocket-provider").info("Connecting to WebSocket", socketUrl)`. This runs through
   `recordOfflineLog` → `persistEntry`, which writes the entry to MMKV **unredacted** (redaction only
   happens in `sanitizeEntryForExport`, called from `getOfflineLogText`/`getOfflineCrashText` at
   export/clipboard-copy time, not at write time). So:
   - The raw, live, password-equivalent `serverAuthToken` (`SHA256(password)`, sent statically on every
     request as `X-Seanime-Token` — see `hashServerPassword`/`getServerAuthHeaders`) and the per-user
     `sessionToken` (sent as `Authorization: Bearer`) sit in plaintext in the on-device offline-log ring
     buffer (200 entries) for as long as they survive eviction.
   - Separately, and worse: `redactSensitiveText` (offline-logger.ts:99-108) has an explicit allowlist of
     query/JSON keys to scrub — `token|proof|auth|authorization|access_token|refresh_token|api_key|apikey`
     — which does NOT include `session`. So even the "sanitized" export path (used by
     `copyOfflineLogsToClipboard()` / the in-app "copy logs" support flow) leaves `&session=<sessionToken>`
     in cleartext. A user who taps "copy debug logs" to paste into a support DM/GitHub issue exports their
     live per-user bearer token unredacted, while believing tokens were scrubbed (the sibling `token=`
     param IS redacted, creating a false sense of safety).
   - Trigger frequency is high, not theoretical: `connectWebSocket()` fires on mount, on every `close`
     event (auto-reconnect with backoff), and on every `AppState` transition to `active` (i.e., every
     foreground) — so live tokens get re-logged repeatedly during normal use, including right before a
     support-log export.

2. **HIGH** — `src/atoms/server.atoms.ts` (`serverAuthTokenAtom`, `sessionTokenAtom`) persist via
   `createAtomStorage()` → `src/atoms/storage.ts` → `createMMKV({ id: "seanime-mobile" })` with no
   `encryptionKey` passed (confirmed: no `createMMKV` call site anywhere in `src/` passes one — grepped
   all 10 call sites). Both the server-password-equivalent hash (`sea-server-auth-token`) and the
   per-user login bearer token (`sea-session-token`) are therefore stored as **plaintext on disk**, not
   in iOS Keychain (`expo-secure-store` is not used anywhere in the app — grepped, zero hits). Neither
   token has any expiry/rotation visible client-side (`getStoredServerAuthToken`/`getStoredSessionToken`
   are read back verbatim and reused indefinitely until an explicit logout or an UNAUTHENTICATED response
   clears them). Anyone with a local (unencrypted) iOS backup of the device, or jailbreak/file access,
   can read both credentials directly from the MMKV file — no on-device authentication (Face ID/passcode)
   gates it, unlike Keychain-backed storage which is excluded from unencrypted backups. This directly
   matches the audit brief's explicit callout ("server password/token storage: SecureStore vs
   AsyncStorage vs plain") — the app uses plain.

## Rejected / near-misses (not reported)

- **`android usesCleartextTraffic: true` in app.config.ts + `withAndroidLanCleartext.js` plugin** — this
  is explicitly by-design (self-hosted server on a home LAN, often plain `http://`), and Android is not a
  shipped target per the task brief ("iOS is the only shipped target; android dirs in modules/ are not
  built"). Not reported.
- **`set-server-url.tsx` accepts `http://` server URLs with no forced-HTTPS / no warning** — also by
  design for the self-hosted/LAN use case (same as web/Denshi clients); the password hash and session
  token are sent over whatever transport the user's own server exposes. This is an inherent trade-off of
  a self-hosted app pointed at a user-chosen URL, not a Tenji-specific defect, and flagging "your
  self-hosted server should use TLS" is a deployment/environment concern, not app code that is
  "broken/half-wired." Considered but excluded per exclusion (b)-style reasoning (design tradeoff, not a
  bug) — kept severity mental note but did not file as a finding since I could not point to concrete
  Tenji code that mishandles TLS (it correctly uses `wss://` when `serverUrlProtocol === "https:"` and
  `ws://` otherwise — see websocket-provider.tsx:67 — so it's not silently downgrading a user's https
  server to plaintext).
- **Stream/download URLs with `appendServerHMACToken` handed to third-party apps via
  `Linking.openURL` (VLC/Infuse/OutPlayer/etc., `src/lib/player/external-players.ts`)** — this is the
  intended mechanism for the "open in external player" feature: a short-TTL (24h), single-endpoint-scoped
  HMAC token, not the raw password/session credential. Sharing a scoped capability URL with a
  user-selected external player app is the feature working as designed, not a leak. Not reported.
- **OTA update integrity (`src/lib/ota/updates.ts`)** — no custom fetch/verification code; relies
  entirely on `expo-updates`' built-in manifest signature verification against the EAS project
  (`EAS_PROJECT_ID` in app.config.ts, channel pinned to `stable` via `requestHeaders`). Found no
  code-signing disablement, no custom insecure fetch of update payloads, nothing to flag. `console.error`
  calls at updates.ts:96/185 only log `Error` objects from `expo-updates` calls (network/parse failures),
  not credentials.
- **Deep-link parameter injection** — no `Linking.addEventListener`/`Linking.getInitialURL` handler
  exists anywhere in the app (grepped `app/_layout.tsx` and the whole tree); expo-router's default
  file-based `scheme: "seanime"` linking is the only deep-link surface, and I found no screen that takes
  an incoming URL param and feeds it into `fetch`, a WebView, `eval`, or similar. The only
  `useLocalSearchParams` consumers are internal navigation params (`id`, `initialView`, `type`) used to
  build typed API queries, not raw URL/HTML content. No WebView component exists in the app at all
  (grepped, zero hits). Nothing to report.
- **`app.config.ts` / `eas.json` secrets** — `EAS_PROJECT_ID` is a public project identifier (not a
  secret; same class of value as a Sentry DSN), no auth tokens, no API keys, no `.env` values inlined.
  `eas.json` has no committed credentials. Nothing to report.
- **`console.error(entry.listData?.progress, ...)` in server-download-modal.tsx:189** — logs local
  playback-progress/episode debug data, not credentials. Passed through the same
  `patchConsole`-wrapped `console.log`/`console.error`, so it does land in the offline log store, but
  contains no secret material. Not reported (out of scope: not a credential).
- **`hashServerPassword` = plain `SHA256(password)` with no salt/PBKDF** — this is a client-side detail
  whose actual security property (whether the server treats it as a bare equality-checked bearer secret
  vs. something hardened server-side) is a server-repo concern, explicitly out of scope ("cross-checking
  endpoint/event semantics only, do not audit it"). Flagged the *consequence* (this hash is a static,
  reusable bearer credential that gets logged/stored in plaintext) as findings #1/#2 above instead of the
  hashing scheme itself.
- **MMKV files not using iOS Data Protection class overrides** — react-native-mmkv doesn't expose a
  file-protection-class option Tenji could call; this is a library-level constraint, not something wrong
  in Tenji's own code beyond "should have used SecureStore instead," which is finding #2.

## Coverage

Read every file under `src/` and `app/` that touches auth/token/session/password/storage/logging/OTA/
deep-linking/config, per the grep sweeps listed above (not a sample — the grep patterns were broad enough
that a targeted read of every matching file was feasible and done: server.atoms.ts, storage.ts,
server-auth.ts, client-identity.ts, requests.ts, websocket-provider.tsx, offline-logger.ts, logger.ts,
updates.ts, app.config.ts, eas.json, set-server-url.tsx, server-url.ts, external-players.ts,
download-manager.ts's HMAC slice, local-file-source.ts, app/_layout.tsx, all 10 createMMKV call sites,
all console.*/logger(...) call sites project-wide, all Linking.* call sites, all useLocalSearchParams
consumers). Did not deep-read unrelated feature code (manga reader internals, player UI, list/grid
screens) beyond confirming they don't independently touch tokens/logging/storage in a way the grep sweep
would have missed — the grep-for-token/secret/auth/console sweep across the whole `src/`+`app/` tree is
what gives confidence nothing security-relevant was missed outside the files above.
