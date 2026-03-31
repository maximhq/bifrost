"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function MCPSettingsLayout({ children }: { children: React.ReactNode }) {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	if (!hasMCPGatewayAccess || !hasSettingsAccess) {
		return <NoPermissionView entity="MCP gateway settings" />;
	}
	return <div>{children}</div>;
}
