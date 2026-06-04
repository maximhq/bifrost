import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import EdgeDevicesPage from "./page";

function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Devices, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="edge devices" />;
	}
	return <EdgeDevicesPage />;
}

export const Route = createFileRoute("/workspace/edge-control/devices")({
	component: RouteComponent,
});
