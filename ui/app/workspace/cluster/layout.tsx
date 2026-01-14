"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function ClusterLayout({ children }: { children: React.ReactNode }) {
  const hasClusterAccess = useRbac(RbacResource.Cluster, RbacOperation.View)
  if (!hasClusterAccess) {
    return <NoPermissionView entity="cluster configuration" />
  }
  return <div>{children}</div>
}
