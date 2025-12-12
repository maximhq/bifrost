"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function ConfigLayout({ children }: { children: React.ReactNode }) {
  const hasConfigAccess = useRbac(RbacResource.Settings, RbacOperation.View)
  if (!hasConfigAccess) {
    return <NoPermissionView entity="configuration" />
  }
  return <div>{children}</div>
}
