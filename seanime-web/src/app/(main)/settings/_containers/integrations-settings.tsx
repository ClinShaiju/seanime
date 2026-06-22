import { useLogout } from "@/api/hooks/auth.hooks"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { SettingsCard, SettingsPageHeader } from "@/app/(main)/settings/_components/settings-card"
import { ConfirmationDialog, useConfirmationDialog } from "@/components/shared/confirmation-dialog"
import { Button } from "@/components/ui/button"
import { ANILIST_OAUTH_URL } from "@/lib/server/config"
import React from "react"
import { MdOutlineConnectWithoutContact } from "react-icons/md"
import { SiAnilist } from "react-icons/si"

// IntegrationsSettings is a per-user tab (visible to every user, including admins) for
// connecting external accounts to THIS profile. AniList connect/disconnect goes through
// the per-user /auth/login & /auth/logout endpoints, so each user links their own account.
export function IntegrationsSettings() {
    const status = useServerStatus()
    const { mutate: logout, isPending: isLoggingOut } = useLogout()

    const user = status?.user
    const isConnected = !!user && !user.isSimulated

    const confirmDisconnect = useConfirmationDialog({
        title: "Disconnect AniList",
        description: "Your AniList account will be unlinked from this profile. You can reconnect anytime.",
        onConfirm: () => logout(),
    })

    const handleConnect = React.useCallback(() => {
        const url = status?.anilistClientId
            ? `https://anilist.co/api/v2/oauth/authorize?client_id=${status.anilistClientId}&response_type=token`
            : ANILIST_OAUTH_URL
        window.open(url, "_self")
    }, [status?.anilistClientId])

    return (
        <>
            <SettingsPageHeader
                title="Integrations"
                description="Connect external accounts to this profile."
                icon={MdOutlineConnectWithoutContact}
            />

            <SettingsCard
                title="AniList"
                description="Sync your anime & manga lists, progress, and scores with your AniList account."
            >
                <div className="flex items-center justify-between gap-4 flex-wrap">
                    <div className="flex items-center gap-3">
                        <SiAnilist className={isConnected ? "text-2xl text-[--brand]" : "text-2xl text-[--muted]"} />
                        <div>
                            <p className="font-medium">
                                {isConnected ? `Connected as ${user?.viewer?.name}` : "Not connected"}
                            </p>
                            <p className="text-sm text-[--muted]">
                                {isConnected
                                    ? "AniList account linked to this profile."
                                    : "Connect your AniList account to sync your lists."}
                            </p>
                        </div>
                    </div>
                    {isConnected ? (
                        <Button intent="alert-subtle" onClick={confirmDisconnect.open} loading={isLoggingOut}>
                            Disconnect
                        </Button>
                    ) : (
                        <Button intent="primary" leftIcon={<SiAnilist />} onClick={handleConnect}>
                            Connect with AniList
                        </Button>
                    )}
                </div>
            </SettingsCard>

            <ConfirmationDialog {...confirmDisconnect} />
        </>
    )
}
