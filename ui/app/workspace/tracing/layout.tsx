import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import TracingPage from "./page";

// Tracing is read with its own permission, independent of the gateway's LLM and MCP logs.
function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Tracing, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="traces" />;
	}
	return <TracingPage />;
}

export const Route = createFileRoute("/workspace/tracing")({
	component: RouteComponent,
});