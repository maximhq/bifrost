"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function UserGroupsLayout({ children }: { children: React.ReactNode }) {
  const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View)
  const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View)
  if (!hasCustomersAccess && !hasTeamsAccess) {
    return <NoPermissionView entity="users and groups" />
  }
  return <div>{children}</div>
}
