import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AlertHistoryPage from "./page";

function RouteComponent() {
	const hasAlertingAccess = useRbac(RbacResource.AlertChannels, RbacOperation.View);
	if (!hasAlertingAccess) {
		return <NoPermissionView entity="alerting" />;
	}
	return <AlertHistoryPage />;
}

export const Route = createFileRoute("/workspace/alerting/history")({
	component: RouteComponent,
});