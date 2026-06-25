import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import ComplexityRouterPage from "./page";

function RouteComponent() {
	const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	if (!hasRoutingRulesAccess) {
		return <NoPermissionView entity="complexity router" />;
	}
	return <ComplexityRouterPage />;
}

export const Route = createFileRoute("/workspace/complexity-router")({
	component: RouteComponent,
});