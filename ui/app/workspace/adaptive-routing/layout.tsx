import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AdaptiveRoutingPage from "./page";

function RouteComponent() {
	const hasAdaptiveRouterAccess = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasAdaptiveRouterAccess) {
		return <NoPermissionView entity="adaptive routing" />;
	}
	// Render the dashboard at the base path; defer to child routes (e.g. /settings).
	return childMatches.length === 0 ? <AdaptiveRoutingPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/adaptive-routing")({
	component: RouteComponent,
});