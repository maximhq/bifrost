import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AlertChannelsPage from "./page";

function RouteComponent() {
	const hasAlertingAccess = useRbac(RbacResource.AlertChannels, RbacOperation.View);
	if (!hasAlertingAccess) {
		return <NoPermissionView entity="alerting" />;
	}
	return <AlertChannelsPage />;
}

export const Route = createFileRoute("/workspace/alerting/channels")({
	component: RouteComponent,
});