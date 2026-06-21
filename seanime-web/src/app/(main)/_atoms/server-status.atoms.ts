import { Status } from "@/api/generated/types"
import { atom } from "jotai"
import { atomWithStorage } from "jotai/utils"

export const SERVER_AUTH_TOKEN_STORAGE_KEY = "sea-server-auth-token"

// Per-user session token (multi-user profile system), sent as `Authorization: Bearer`.
// Distinct from the server password (SERVER_AUTH_TOKEN_STORAGE_KEY / X-Seanime-Token).
export const SESSION_TOKEN_STORAGE_KEY = "sea-session-token"

export const serverStatusAtom = atom<Status | undefined>(undefined)

export const isLoginModalOpenAtom = atom(false)

export const serverAuthTokenAtom = atomWithStorage<string | undefined>(SERVER_AUTH_TOKEN_STORAGE_KEY, undefined, undefined, { getOnInit: true })

export const sessionTokenAtom = atomWithStorage<string | undefined>(SESSION_TOKEN_STORAGE_KEY, undefined, undefined, { getOnInit: true })
