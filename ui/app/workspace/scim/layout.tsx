"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function SCIMLayout({ children }: { children: React.ReactNode }) {
  const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View)
  if (!hasUserProvisioningAccess) {
    return <NoPermissionView entity="user provisioning" />
  }
  return <div>{children}</div>
}
