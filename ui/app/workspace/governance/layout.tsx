import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import GovernancePage from "./page";

function RouteComponent() {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasGovernanceAccess) {
		return <NoPermissionView entity="governance" />;
	}
	return childMatches.length === 0 ? <GovernancePage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/governance")({
	component: RouteComponent,
});