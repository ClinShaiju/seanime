import { useUserLogin } from "@/api/hooks/user-auth.hooks"
import { defineSchema, Field, Form } from "@/components/ui/form"
import { Modal } from "@/components/ui/modal"
import React from "react"

// UserLoginScreen is shown on a networked server (one with a server password) when
// no per-user session is present. Mirrors the server-password screen (ServerAuth).
export function UserLoginScreen() {
    const { mutate: login, isPending } = useUserLogin()

    return (
        <Modal
            title="Sign in"
            description="Enter your Seanime username and password."
            open={true}
            onOpenChange={() => {}}
            overlayClass="bg-opacity-100 bg-gray-900"
            contentClass="border focus:outline-none focus-visible:outline-none outline-none"
            hideCloseButton
        >
            <Form
                schema={defineSchema(({ z }) => z.object({
                    username: z.string().min(1, "Username is required"),
                    password: z.string().min(1, "Password is required"),
                }))}
                onSubmit={data => {
                    login({ username: data.username.trim(), password: data.password })
                }}
            >
                <Field.Text
                    name="username"
                    label="Username"
                    autoComplete="username"
                />
                <Field.Text
                    type="password"
                    name="password"
                    label="Password"
                    autoComplete="current-password"
                />
                <Field.Submit loading={isPending}>Sign in</Field.Submit>
            </Form>
        </Modal>
    )
}
