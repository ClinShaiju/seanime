import { getServerBaseUrl } from "@/api/client/server-url"
import { serverAuthTokenAtom } from "@/app/(main)/_atoms/server-status.atoms"
import { defineSchema, Field, Form } from "@/components/ui/form"
import { Modal } from "@/components/ui/modal"
import { useAtom } from "jotai"
import { sha256 } from "js-sha256"
import React, { useState } from "react"

export function ServerAuth() {

    const [, setAuthToken] = useAtom(serverAuthTokenAtom)
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)

    return (<>
        <Modal
            title="Password required"
            description="This Seanime server requires authentication."
            open={true}
            onOpenChange={(v) => {}}
            overlayClass="bg-opacity-100 bg-gray-900"
            contentClass="border focus:outline-none focus-visible:outline-none outline-none"
            hideCloseButton
        >
            <Form
                schema={defineSchema(({ z }) => z.object({
                    password: z.string().min(1, "Password is required"),
                }))}
                onSubmit={async data => {
                    setLoading(true)
                    setError(null)
                    const hash = sha256(data.password)

                    // Validate the password against the server before advancing, so a wrong
                    // password shows an error instead of proceeding to the user-login screen.
                    try {
                        const res = await fetch(getServerBaseUrl() + "/api/v1/status", {
                            headers: { "X-Seanime-Token": hash },
                        })
                        const json = await res.json() as { data?: { serverAuthenticated?: boolean } }
                        if (!json?.data?.serverAuthenticated) {
                            setError("Incorrect password")
                            setLoading(false)
                            return
                        }
                    }
                    catch {
                        setError("Could not reach the server")
                        setLoading(false)
                        return
                    }

                    setAuthToken(hash)
                    React.startTransition(() => {
                        window.location.href = "/"
                        setLoading(false)
                    })
                }}
            >
                <Field.Text
                    type="password"
                    name="password"
                    label="Enter the password"
                    fieldClass=""
                />
                {error && <p className="text-red-400 text-sm">{error}</p>}
                <Field.Submit showLoadingOverlayOnSuccess loading={loading}>Continue</Field.Submit>
            </Form>
        </Modal>
    </>)
}
