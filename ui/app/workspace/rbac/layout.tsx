"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function RBACLayout({ children }: { children: React.ReactNode }) {
  const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View)
  if (!hasRbacAccess) {
    return <NoPermissionView entity="roles and permissions" />
  }
  return <div>{children}</div>
}
