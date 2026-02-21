"use client"

import { useRouter } from "next/navigation"
import { useEffect } from "react"

export default function UserGroupsPage() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/workspace/governance/users")
  }, [router])
  return null
}
