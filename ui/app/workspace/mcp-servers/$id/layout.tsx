import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import MCPServerDetailPage from "./page";

function RouteComponent() {
	const { id } = Route.useParams();
	const canView = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	if (!canView) {
		return <NoPermissionView entity="MCP servers" />;
	}
	return <MCPServerDetailPage id={id} />;
}

export const Route = createFileRoute("/workspace/mcp-servers/$id")({
	component: RouteComponent,
});
