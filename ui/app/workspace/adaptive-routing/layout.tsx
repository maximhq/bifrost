"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function AdaptiveRoutingLayout({ children }: { children: React.ReactNode }) {
  const hasAdaptiveRouterAccess = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View)
  if (!hasAdaptiveRouterAccess) {
    return <NoPermissionView entity="adaptive routing" />
  }
  return <div>{children}</div>
}
