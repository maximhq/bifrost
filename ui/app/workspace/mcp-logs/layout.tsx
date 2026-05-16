import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import MCPLogsPage from "./page";

function RouteComponent() {
	const hasViewMCPLogsAccess = useRbac(RbacResource.MCPLogs, RbacOperation.View);
	if (!hasViewMCPLogsAccess) {
		return <NoPermissionView entity="mcp logs" />;
	}
	return <MCPLogsPage />;
}

export const Route = createFileRoute("/workspace/mcp-logs")({
	component: RouteComponent,
});