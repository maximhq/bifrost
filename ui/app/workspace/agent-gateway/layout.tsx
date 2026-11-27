import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AgentGatewayPage from "./page";

function RouteComponent() {
	const hasAgentGatewayAccess = useRbac(RbacResource.AgentGateway, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasAgentGatewayAccess) {
		return <NoPermissionView entity="Agent gateway configuration" />;
	}
	return childMatches.length === 0 ? <AgentGatewayPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/agent-gateway")({
	component: RouteComponent,
});