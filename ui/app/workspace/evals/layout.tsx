import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute, Navigate, Outlet, useChildMatches } from "@tanstack/react-router";

function RouteComponent() {
	const hasEvaluatorsAccess = useRbac(RbacResource.Evaluators, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasEvaluatorsAccess) {
		return <NoPermissionView entity="evaluators" />;
	}
	// Evals has no page of its own yet: it opens on its first section.
	return childMatches.length === 0 ? <Navigate to="/workspace/evals/evaluators" replace /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/evals")({
	component: RouteComponent,
});