"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { NoPermissionView } from "@/components/noPermissionView"
import { useGetCoreConfigQuery } from "@/lib/store"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function ConfigLayout({ children }: { children: React.ReactNode }) {
  const hasConfigAccess = useRbac(RbacResource.Settings, RbacOperation.View)
  const { isLoading } = useGetCoreConfigQuery({ fromDB: true })

  if (!hasConfigAccess) {
    return <NoPermissionView entity="configuration" />
  }

  if (isLoading) {
    return <FullPageLoader />
  }

  return <div>{children}</div>
}
