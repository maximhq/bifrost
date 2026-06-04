import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import EdgeConfigPage from "./page";

function RouteComponent() {
	const hasAccess = useRbac(RbacResource.EdgeConfig, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="edge settings" />;
	}
	return <EdgeConfigPage />;
}

export const Route = createFileRoute("/workspace/edge-control/config")({
	component: RouteComponent,
});
