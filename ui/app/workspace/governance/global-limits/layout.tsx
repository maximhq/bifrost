import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import GlobalLimitsPage from "./page";

function RouteComponent() {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	if (!hasGovernanceAccess) {
		return <NoPermissionView entity="global budget" />;
	}
	return <GlobalLimitsPage />;
}

export const Route = createFileRoute("/workspace/governance/global-limits")({
	component: RouteComponent,
});