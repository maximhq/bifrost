"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function ObservabilityLayout({ children }: { children: React.ReactNode }) {
  const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View)
  if (!hasObservabilityAccess) {
    return <NoPermissionView entity="observability settings" />
  }
  return <div>{children}</div>
}
