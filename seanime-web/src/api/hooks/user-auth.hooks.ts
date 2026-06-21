import { useServerMutation, useServerQuery } from "@/api/client/requests"
import { sessionTokenAtom } from "@/app/(main)/_atoms/server-status.atoms"
import { useQueryClient } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { toast } from "sonner"

export const USER_LIST_QUERY_KEY = "user-list"

export type UserLoginVariables = { username: string, password: string }

export type SeaUser = {
    id: number
    username: string
    role: string
    anilistAccountId?: number
}

export type UserLoginResponse = {
    token: string
    user: SeaUser
}

// useUserLogin authenticates a Seanime user (username + password) behind the
// server-password gate and stores the returned session token for `Authorization: Bearer`.
export function useUserLogin() {
    const setSessionToken = useSetAtom(sessionTokenAtom)

    return useServerMutation<UserLoginResponse, UserLoginVariables>({
        endpoint: "/api/v1/user/login",
        method: "POST",
        mutationKey: ["user-login"],
        onSuccess: data => {
            if (data?.token) {
                setSessionToken(data.token)
                toast.success("Signed in")
                // Full reload so every query refetches with the new session.
                window.location.href = "/"
            }
        },
    })
}

// useListUsers fetches all Seanime users (admin only).
export function useListUsers() {
    return useServerQuery<SeaUser[]>({
        endpoint: "/api/v1/user/list",
        method: "GET",
        queryKey: [USER_LIST_QUERY_KEY],
    })
}

export type RegisterUserVariables = { username: string, password: string, role: string }

// useRegisterUser creates a new Seanime user (admin only).
export function useRegisterUser() {
    const queryClient = useQueryClient()
    return useServerMutation<SeaUser, RegisterUserVariables>({
        endpoint: "/api/v1/user/register",
        method: "POST",
        mutationKey: ["user-register"],
        onSuccess: async () => {
            toast.success("User created")
            await queryClient.invalidateQueries({ queryKey: [USER_LIST_QUERY_KEY] })
        },
    })
}

// useUserLogout revokes the current session token and clears it locally.
export function useUserLogout() {
    const setSessionToken = useSetAtom(sessionTokenAtom)

    return useServerMutation<boolean>({
        endpoint: "/api/v1/user/logout",
        method: "POST",
        mutationKey: ["user-logout"],
        onSuccess: () => {
            setSessionToken(undefined)
            toast.success("Signed out")
            window.location.href = "/"
        },
    })
}
