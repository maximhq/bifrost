"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function AuditLogsLayout({ children }: { children: React.ReactNode }) {
  const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View)
  if (!hasAuditLogsAccess) {
    return <NoPermissionView entity="audit logs" />
  }
  return <div>{children}</div>
}
