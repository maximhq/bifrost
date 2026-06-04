import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import EdgeInventoryPage from "./page";

function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Inventory, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="edge inventory" />;
	}
	return <EdgeInventoryPage />;
}

export const Route = createFileRoute("/workspace/edge-control/inventory")({
	component: RouteComponent,
});
