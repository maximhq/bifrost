import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import RelayLogsPage from "./page";

// Relay Logs are read with the RelayLogs permission, independent of the LLM Logs resource.
function RouteComponent() {
	const hasAccess = useRbac(RbacResource.RelayLogs, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="relay logs" />;
	}
	return <RelayLogsPage />;
}

export const Route = createFileRoute("/workspace/relay-logs")({
	component: RouteComponent,
});