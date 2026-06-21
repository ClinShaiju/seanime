import { useUserLogin } from "@/api/hooks/user-auth.hooks"
import { AppLayoutStack } from "@/components/ui/app-layout"
import { Card } from "@/components/ui/card"
import { defineSchema, Field, Form } from "@/components/ui/form"
import React from "react"

// UserLoginScreen is shown on a networked server (one with a server password) when
// no per-user session is present. Configuring the server requires logging in as the
// admin; regular users log in to load their own profile.
export function UserLoginScreen() {
    const { mutate: login, isPending } = useUserLogin()

    return (
        <div className="container max-w-md py-10">
            <Card className="md:py-10">
                <AppLayoutStack>
                    <div className="text-center space-y-4">
                        <div className="mb-4 flex justify-center w-full">
                            <img src="/seanime-logo.png" alt="logo" className="w-24 h-auto" />
                        </div>
                        <h3>Sign in</h3>
                        <p className="text-[--muted] text-sm">
                            Enter your Seanime username and password.
                        </p>

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
                    </div>
                </AppLayoutStack>
            </Card>
        </div>
    )
}
