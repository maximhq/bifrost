import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import GuardrailsPage from "./page";

function RouteComponent() {
	const hasGuardrailsAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasGuardrailsAccess) {
		return <NoPermissionView entity="guardrails configuration" />;
	}
	return childMatches.length === 0 ? <GuardrailsPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/guardrails")({
	component: RouteComponent,
});