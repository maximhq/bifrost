"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function PluginsLayout({ children }: { children: React.ReactNode }) {
  const hasPluginsAccess = useRbac(RbacResource.Plugins, RbacOperation.View)
  if (!hasPluginsAccess) {
    return <NoPermissionView entity="plugins" />
  }
  return <div>{children}</div>
}