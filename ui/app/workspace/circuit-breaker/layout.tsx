import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import CircuitBreakerPage from "./page";

function RouteComponent() {
	const hasAccess = useRbac(RbacResource.CircuitBreaker, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasAccess) {
		return <NoPermissionView entity="circuit breaker" />;
	}
	return childMatches.length === 0 ? <CircuitBreakerPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/circuit-breaker")({
	component: RouteComponent,
});