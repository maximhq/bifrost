import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import PluginsPage from "./page";

function RouteComponent() {
	const hasPluginsAccess = useRbac(RbacResource.Plugins, RbacOperation.View);
	if (!hasPluginsAccess) {
		return <NoPermissionView entity="plugins" />;
	}
	return <PluginsPage />;
}

export const Route = createFileRoute("/workspace/plugins")({
	component: RouteComponent,
});