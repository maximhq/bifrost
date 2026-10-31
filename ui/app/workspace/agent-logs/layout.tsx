import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import AgentLogsPage from "./page";

function RouteComponent() {
	const hasViewAgentLogsAccess = useRbac(RbacResource.AgentLogs, RbacOperation.View);
	if (!hasViewAgentLogsAccess) {
		return <NoPermissionView entity="agent logs" />;
	}
	return <AgentLogsPage />;
}

export const Route = createFileRoute("/workspace/agent-logs")({
	component: RouteComponent,
});