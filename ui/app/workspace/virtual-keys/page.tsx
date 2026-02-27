"use client"

import { useRouter } from "next/navigation"
import { useEffect } from "react"

export default function VirtualKeysRedirectPage() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/workspace/governance/virtual-keys")
  }, [router])
  return null
}
