import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";

function RouteComponent() {
	const hasFinOpsAccess = useRbac(RbacResource.FinOps, RbacOperation.View);
	if (!hasFinOpsAccess) {
		return <NoPermissionView entity="FinOps" />;
	}
	return <Outlet />;
}

export const Route = createFileRoute("/workspace/finops")({
	beforeLoad: ({ location }) => {
		if (location.pathname === "/workspace/finops" || location.pathname === "/workspace/finops/") {
			throw redirect({ to: "/workspace/finops/overview", replace: true });
		}
	},
	component: RouteComponent,
});