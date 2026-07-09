import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import MCPServersPage from "./page";

function RouteComponent() {
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasMCPGatewayAccess) {
		return <NoPermissionView entity="MCP gateway configuration" />;
	}
	return childMatches.length === 0 ? <MCPServersPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/mcp-registry")({
	component: RouteComponent,
});