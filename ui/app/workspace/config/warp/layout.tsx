import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import WarpPage from "./page";

function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Warp, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="Warp" />;
	}
	return <WarpPage />;
}

export const Route = createFileRoute("/workspace/config/warp")({
	component: RouteComponent,
});
