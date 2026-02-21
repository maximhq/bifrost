"use client"

import { usePathname } from "next/navigation"
import { useRouter } from "next/navigation"
import { useEffect } from "react"

export default function UserGroupsLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter()
  const pathname = usePathname()

  useEffect(() => {
    if (pathname === "/workspace/user-groups") return
    const governancePath = pathname.replace("/workspace/user-groups", "/workspace/governance")
    router.replace(governancePath)
  }, [pathname, router])

  return <>{children}</>
}
