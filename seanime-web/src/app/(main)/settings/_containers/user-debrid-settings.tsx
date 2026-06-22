import { useServerMutation } from "@/api/client/requests"
import { API_ENDPOINTS } from "@/api/generated/endpoints"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { AutoSelectProfileButton } from "@/app/(main)/settings/_components/autoselect-profile-form"
import { SettingsCard, SettingsPageHeader } from "@/app/(main)/settings/_components/settings-card"
import { defineSchema, Field, Form } from "@/components/ui/form"
import { useQueryClient } from "@tanstack/react-query"
import React from "react"
import { useFormContext } from "react-hook-form"
import { HiOutlineServerStack } from "react-icons/hi2"
import { toast } from "sonner"

const DEBRID_PROVIDER_OPTIONS = [
    { label: "None", value: "-" },
    { label: "TorBox", value: "torbox" },
    { label: "Real-Debrid", value: "realdebrid" },
    { label: "AllDebrid", value: "alldebrid" },
]

// UserDebridSettings is the non-admin Debrid tab: a "Use server debrid" toggle, and —
// when off — the user's own provider + API key. (Functional streaming through the
// user's own debrid lands with the per-user streaming work; this stores the choice.)
export function UserDebridSettings() {
    const serverStatus = useServerStatus()
    const queryClient = useQueryClient()
    const ud = serverStatus?.userDebrid

    const { mutate: save, isPending } = useServerMutation<boolean,
        { useServerDebrid: boolean, provider: string, apiKey: string, useServerAutoSelect: boolean }>({
        endpoint: "/api/v1/user/debrid",
        method: "PATCH",
        mutationKey: ["user-debrid"],
        onSuccess: async () => {
            await queryClient.invalidateQueries({ queryKey: [API_ENDPOINTS.STATUS.GetStatus.key] })
            toast.success("Debrid settings saved")
        },
    })

    return (
        <>
            <SettingsPageHeader
                title="Debrid Service"
                description="Use the server's shared debrid, or bring your own."
                icon={HiOutlineServerStack}
            />

            <Form
                schema={defineSchema(({ z }) => z.object({
                    useServerDebrid: z.boolean(),
                    provider: z.string(),
                    apiKey: z.string().optional(),
                    useServerAutoSelect: z.boolean(),
                }))}
                defaultValues={{
                    useServerDebrid: ud?.useServerDebrid ?? true,
                    provider: ud?.provider || "-",
                    apiKey: "",
                    useServerAutoSelect: ud?.useServerAutoSelect ?? true,
                }}
                onSubmit={data => save({
                    useServerDebrid: data.useServerDebrid,
                    provider: data.provider === "-" ? "" : data.provider,
                    apiKey: data.apiKey ?? "",
                    useServerAutoSelect: data.useServerAutoSelect,
                })}
            >
                <SettingsCard>
                    <Field.Switch
                        side="right"
                        name="useServerDebrid"
                        label="Use server debrid"
                        help="Stream through the server's shared debrid account. Turn this off to use your own debrid provider."
                    />
                </SettingsCard>

                <UserDebridOwnFields hasApiKey={!!ud?.hasApiKey} />

                <SettingsCard
                    title="Auto-select"
                    description="How a torrent is automatically picked when you stream via debrid."
                >
                    <Field.Switch
                        side="right"
                        name="useServerAutoSelect"
                        label="Use server default auto-select"
                        help="Use the server's auto-select preferences. Turn this off to set your own (resolution, language, ranking, etc.)."
                    />
                    <UserDebridAutoSelectFields />
                </SettingsCard>

                <Field.Submit loading={isPending}>Save</Field.Submit>
            </Form>
        </>
    )
}

function UserDebridOwnFields({ hasApiKey }: { hasApiKey: boolean }) {
    const f = useFormContext()
    if (f.watch("useServerDebrid")) return null
    return (
        <SettingsCard>
            <Field.Select name="provider" label="Provider" options={DEBRID_PROVIDER_OPTIONS} />
            <Field.Text
                name="apiKey"
                label="API Key"
                type="password"
                help={hasApiKey ? "A key is saved. Leave blank to keep it." : undefined}
            />
        </SettingsCard>
    )
}

// UserDebridAutoSelectFields shows the full auto-select profile editor (resolution,
// language, codecs, ranking, …) when the user opts out of the server default. The
// editor self-fetches via the /auto-select/profile API, which is per-user, so it
// edits THIS user's own profile. Save the toggle first to persist the choice.
function UserDebridAutoSelectFields() {
    const f = useFormContext()
    if (f.watch("useServerAutoSelect")) return null
    return (
        <div className="pt-2">
            <AutoSelectProfileButton />
        </div>
    )
}
