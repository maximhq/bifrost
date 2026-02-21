"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function CustomPricingLayout({ children }: { children: React.ReactNode }) {
  const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View)
  if (!hasSettingsAccess) {
    return <NoPermissionView entity="custom pricing" />
  }
  return <>{children}</>
}
