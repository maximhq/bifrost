import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import MCPLibraryPage from "./page";

function RouteComponent() {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	if (!hasMCPGatewayAccess) {
		return <NoPermissionView entity="MCP gateway library" />;
	}
	return <MCPLibraryPage />;
}

export const Route = createFileRoute("/workspace/mcp-registry/library")({
	component: RouteComponent,
});