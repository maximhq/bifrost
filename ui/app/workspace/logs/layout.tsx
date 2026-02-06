"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function LogsLayout({ children }: { children: React.ReactNode }) {
  const hasViewLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View)
  if (!hasViewLogsAccess) {
    return <NoPermissionView entity="logs" />
  }
  return (
    <div className="flex flex-col h-full">
      {children}
    </div>
  )
}
