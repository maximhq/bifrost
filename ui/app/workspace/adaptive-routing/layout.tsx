import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AdaptiveRoutingPage from "./page";

function RouteComponent() {
	const hasAdaptiveRouterAccess = useRbac(RbacResource.AdaptiveRouter, RbacOperation.View);
	if (!hasAdaptiveRouterAccess) {
		return <NoPermissionView entity="adaptive routing" />;
	}
	return <AdaptiveRoutingPage />;
}

export const Route = createFileRoute("/workspace/adaptive-routing")({
	component: RouteComponent,
});