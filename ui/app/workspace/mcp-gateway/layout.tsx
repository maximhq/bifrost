"use client";

import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function MCPGatewayLayout({ children }: { children: React.ReactNode }) {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	if (!hasMCPGatewayAccess) {
		return <div>You don't have permission to view MCP gateway configuration.</div>;
	}
	return <div>{children}</div>;
}
