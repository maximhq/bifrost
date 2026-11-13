import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import ModelRoutersPage from "./page";

// The model router APIs are gated on Governance, so the page is too.
function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="model routers" />;
	}
	return <ModelRoutersPage />;
}

export const Route = createFileRoute("/workspace/model-routers")({
	component: RouteComponent,
});