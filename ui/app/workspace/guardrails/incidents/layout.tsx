import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import GuardrailIncidentsPage from "./page";

// Incidents are log rows, so viewing them needs log access on top of the
// guardrails access the parent route already checks.
function RouteComponent() {
	const hasLogsAccess = useRbac(RbacResource.Logs, RbacOperation.View);
	if (!hasLogsAccess) {
		return <NoPermissionView entity="guardrail incidents" />;
	}
	return <GuardrailIncidentsPage />;
}

export const Route = createFileRoute("/workspace/guardrails/incidents")({
	component: RouteComponent,
});