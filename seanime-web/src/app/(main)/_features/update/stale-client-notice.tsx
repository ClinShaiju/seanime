import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { Button } from "@/components/ui/button"
import { Modal } from "@/components/ui/modal"
import { __buildVersion__ } from "@/types/constants"
import React from "react"
import { BiRefresh } from "react-icons/bi"

/**
 * Shows a "reload to update" popup when the JS running in this tab is older than
 * the server it is talking to. This happens when the server binary is updated
 * (self-update or VPS redeploy) while a browser tab / PWA was left open: the tab
 * keeps the previously-served bundle, so a hard refresh is needed to pick up the
 * new web build. Detected by comparing the version baked into this bundle at
 * build time against the version the server reports in /status.
 *
 * No-ops in dev (no baked version) and on Denshi, where electron-updater replaces
 * the whole app and reloads the renderer itself.
 */
export function StaleClientNotice() {
    const serverStatus = useServerStatus()
    const [dismissed, setDismissed] = React.useState(false)

    const serverVersion = serverStatus?.version
    const isStale = !!__buildVersion__ && !!serverVersion && __buildVersion__ !== serverVersion

    // Reset the dismissal whenever the server version changes again.
    React.useEffect(() => {
        setDismissed(false)
    }, [serverVersion])

    if (!isStale) return null

    return (
        <Modal
            open={!dismissed}
            onOpenChange={v => setDismissed(!v)}
            title="Update available"
            contentClass="max-w-md"
        >
            <div className="space-y-4">
                <p className="text-[--muted]">
                    The server was updated to <span className="font-semibold text-white">{serverVersion}</span> while this
                    page was open (running <span className="font-semibold">{__buildVersion__}</span>). Reload to load the
                    new version — some changes won't show until you do.
                </p>
                <div className="flex gap-2 justify-end">
                    <Button intent="white-subtle" onClick={() => setDismissed(true)}>Later</Button>
                    <Button leftIcon={<BiRefresh className="text-xl" />} onClick={() => window.location.reload()}>
                        Reload now
                    </Button>
                </div>
            </div>
        </Modal>
    )
}
