import { useIsAdmin } from "@/app/(main)/_hooks/use-server-status"
import React from "react"

// AdminGate renders its children only for admins (multi-user profiles). Use it to hide
// admin-only settings sections/controls from regular users (the section/control-level
// counterpart to hiding whole tabs).
export function AdminGate({ children }: { children?: React.ReactNode }) {
    const isAdmin = useIsAdmin()
    if (!isAdmin) return null
    return <>{children}</>
}
