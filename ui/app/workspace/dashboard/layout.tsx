"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  const hasObservabilityAccess = useRbac(RbacResource.Observability, RbacOperation.View)
  if (!hasObservabilityAccess) {
    return <NoPermissionView entity="dashboard" />;
  }
  return <div>{children}</div>
}
