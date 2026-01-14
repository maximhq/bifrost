"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function VirtualKeysLayout({ children }: { children: React.ReactNode }) {
  const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View)
  if (!hasVirtualKeysAccess) {
    return <NoPermissionView entity="virtual keys" />
  }
  return <div>{children}</div>
}
