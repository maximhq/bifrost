import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import MCPSettingsPage from "./page";

function RouteComponent() {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	if (!hasMCPGatewayAccess || !hasSettingsAccess) {
		return <NoPermissionView entity="MCP gateway settings" />;
	}
	return <MCPSettingsPage />;
}

export const Route = createFileRoute("/workspace/mcp-settings")({
	component: RouteComponent,
});