import { serverAuthTokenAtom } from "@/app/(main)/_atoms/server-status.atoms"
import { QueryObserverBaseResult } from "@tanstack/react-query"
import { useAtomValue } from "jotai/react"
import React from "react"

/**
 * Stale-while-revalidate across page loads for a few heavy collection queries: the last
 * successful response is snapshotted to localStorage and served as placeholderData on the
 * next cold start, so the home/library paints instantly while the real fetch runs.
 *
 * Keyed by a hash of the auth token so one browser profile never flashes another user's
 * library after an account switch. Best-effort: quota/parse failures just skip the snapshot.
 */

function tokenHash(token: string | null | undefined): string {
    const s = token || "local"
    let h = 5381
    for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0
    return (h >>> 0).toString(36)
}

function storageKey(key: string, token: string | null | undefined) {
    return `sea-snapshot-${key}-${tokenHash(token)}`
}

export function loadQuerySnapshot<T>(key: string, token: string | null | undefined): T | undefined {
    try {
        const raw = localStorage.getItem(storageKey(key, token))
        return raw ? JSON.parse(raw) as T : undefined
    }
    catch {
        return undefined
    }
}

export function saveQuerySnapshot(key: string, token: string | null | undefined, data: unknown) {
    const write = () => {
        try {
            localStorage.setItem(storageKey(key, token), JSON.stringify(data))
        }
        catch {
            // quota exceeded / private mode — snapshot is best-effort
        }
    }
    // Big libraries serialize to megabytes; keep it off the critical path.
    if (typeof requestIdleCallback === "function") requestIdleCallback(write)
    else setTimeout(write, 500)
}

/**
 * Wire a query result to the snapshot store: returns placeholderData for the query and
 * persists fresh (non-placeholder) data as it arrives.
 */
export function useQuerySnapshot<T>(key: string, query: Pick<QueryObserverBaseResult<T>, "data" | "isPlaceholderData">) {
    const token = useAtomValue(serverAuthTokenAtom)
    React.useEffect(() => {
        if (query.data !== undefined && !query.isPlaceholderData) {
            saveQuerySnapshot(key, token, query.data)
        }
    }, [query.data, query.isPlaceholderData, key, token])
}

export function useSnapshotPlaceholder<T>(key: string): () => T | undefined {
    const token = useAtomValue(serverAuthTokenAtom)
    return React.useCallback(() => loadQuerySnapshot<T>(key, token), [key, token])
}
