import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

function RouteComponent() {
	const hasAlertingAccess = useRbac(RbacResource.AlertChannels, RbacOperation.View);

	if (!hasAlertingAccess) {
		return <NoPermissionView entity="alerting" />;
	}

	return <div className="flex h-full flex-col">{<Outlet />}</div>;
}

export const Route = createFileRoute("/workspace/alerting")({
	beforeLoad: ({ location }) => {
		if (location.pathname === "/workspace/alerting" || location.pathname === "/workspace/alerting/") {
			throw redirect({ to: "/workspace/alerting/channels", replace: true });
		}
	},
	component: RouteComponent,
});