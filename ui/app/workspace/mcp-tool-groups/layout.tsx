import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import MCPToolGroupsPage from "./page";

function RouteComponent() {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	if (!hasMCPGatewayAccess) {
		return <NoPermissionView entity="MCP tool groups" />;
	}
	return <MCPToolGroupsPage />;
}

export const Route = createFileRoute("/workspace/mcp-tool-groups")({
	component: RouteComponent,
});
