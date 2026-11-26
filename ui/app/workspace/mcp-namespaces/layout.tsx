import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import NamespacesPage from "./page";

function RouteComponent() {
	const canView = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!canView) {
		return <NoPermissionView entity="MCP namespaces" />;
	}
	return childMatches.length === 0 ? <NamespacesPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/mcp-namespaces")({
	component: RouteComponent,
});
