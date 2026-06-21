import { useSeaQuery } from "@/api/client/requests"
import { SeaUser, useListUsers, useRegisterUser, USER_LIST_QUERY_KEY } from "@/api/hooks/user-auth.hooks"
import { SettingsPageHeader } from "@/app/(main)/settings/_components/settings-card"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { defineSchema, Field, Form } from "@/components/ui/form"
import { useQueryClient } from "@tanstack/react-query"
import React from "react"
import { LuTrash2, LuUserPlus, LuUsers } from "react-icons/lu"
import { toast } from "sonner"

// UsersSettings is the admin-only Users management section: list, create, and delete
// Seanime users. Backend endpoints (/api/v1/user/list|register|:id) are admin-gated.
export function UsersSettings() {
    const { data: users, isLoading } = useListUsers()
    const { mutate: registerUser, isPending: isRegistering } = useRegisterUser()
    const { seaFetch } = useSeaQuery()
    const queryClient = useQueryClient()
    const [deletingId, setDeletingId] = React.useState<number | null>(null)
    const [formKey, setFormKey] = React.useState(0) // bump to remount (clear) the add form

    async function handleDelete(id: number) {
        setDeletingId(id)
        try {
            await seaFetch(`/api/v1/user/${id}`, "DELETE")
            toast.success("User deleted")
            await queryClient.invalidateQueries({ queryKey: [USER_LIST_QUERY_KEY] })
        }
        catch {
            toast.error("Failed to delete user")
        }
        finally {
            setDeletingId(null)
        }
    }

    return (
        <>
            <SettingsPageHeader
                title="Users"
                description="Manage who can sign in to this server. Each user gets their own profile."
                icon={LuUsers}
            />

            <Card className="p-4 space-y-3">
                <h4>Add a user</h4>
                <Form
                    key={formKey}
                    schema={defineSchema(({ z }) => z.object({
                        username: z.string().min(1, "Username is required"),
                        password: z.string().min(6, "Password must be at least 6 characters"),
                        role: z.string(),
                    }))}
                    defaultValues={{ role: "user" }}
                    onSubmit={data => {
                        registerUser(
                            { username: data.username.trim(), password: data.password, role: data.role },
                            { onSuccess: () => setFormKey(k => k + 1) }, // clear fields after add
                        )
                    }}
                >
                    <div className="flex flex-col md:flex-row gap-3 md:items-end">
                        <Field.Text name="username" label="Username" autoComplete="off" />
                        <Field.Text type="password" name="password" label="Password" autoComplete="new-password" />
                        <Field.Select
                            name="role"
                            label="Role"
                            options={[
                                { value: "user", label: "User" },
                                { value: "admin", label: "Admin" },
                            ]}
                        />
                        <Field.Submit loading={isRegistering} leftIcon={<LuUserPlus />}>Add</Field.Submit>
                    </div>
                </Form>
            </Card>

            <Card className="p-4 space-y-2">
                <h4>Existing users</h4>
                {isLoading ? (
                    <p className="text-[--muted] text-sm">Loading…</p>
                ) : !users?.length ? (
                    <p className="text-[--muted] text-sm">No users yet.</p>
                ) : (
                    <ul className="divide-y divide-[--border]">
                        {users.map((u: SeaUser) => (
                            <li key={u.id} className="flex items-center justify-between py-2">
                                <span className="flex items-center gap-2">
                                    <span className="font-medium">{u.username}</span>
                                    <span className="text-xs uppercase text-[--muted] border border-[--border] rounded px-1.5 py-0.5">
                                        {u.role}
                                    </span>
                                </span>
                                {u.role !== "admin" && (
                                    <Button
                                        size="sm"
                                        intent="alert-basic"
                                        leftIcon={<LuTrash2 />}
                                        loading={deletingId === u.id}
                                        onClick={() => handleDelete(u.id)}
                                    >
                                        Delete
                                    </Button>
                                )}
                            </li>
                        ))}
                    </ul>
                )}
            </Card>
        </>
    )
}
