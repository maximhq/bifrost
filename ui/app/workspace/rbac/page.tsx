"use client"

import { useRouter } from "next/navigation"
import { useEffect } from "react"

export default function RBACRedirectPage() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/workspace/governance/rbac")
  }, [router])
  return null
}
