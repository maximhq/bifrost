import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import DashboardPage from "./page";

function RouteComponent() {
	const hasDashboardAccess = useRbac(RbacResource.Dashboard, RbacOperation.View);
	if (!hasDashboardAccess) {
		return <NoPermissionView entity="dashboard" />;
	}
	return <DashboardPage />;
}

export const Route = createFileRoute("/workspace/dashboard")({
	component: RouteComponent,
});