import { createFileRoute, Outlet, redirect, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

function RouteComponent() {
	const hasAlertingAccess = useRbac(RbacResource.Observability, RbacOperation.View);
	const childMatches = useChildMatches();

	if (!hasAlertingAccess) {
		return <NoPermissionView entity="alerting" />;
	}

	if (childMatches.length === 0) {
		throw redirect({ to: "/workspace/alerting/channels", replace: true });
	}

	return <div className="flex h-full flex-col">{<Outlet />}</div>;
}

export const Route = createFileRoute("/workspace/alerting")({
	component: RouteComponent,
});
