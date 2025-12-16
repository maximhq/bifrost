"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function MCPGatewayLayout({ children }: { children: React.ReactNode }) {
  const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View)
  if (!hasMCPGatewayAccess) {
    return <NoPermissionView entity="MCP gateway configuration" />
  }
  return <div>{children}</div>
}
