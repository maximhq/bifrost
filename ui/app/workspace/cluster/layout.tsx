import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

function RouteComponent() {
	const hasClusterAccess = useRbac(RbacResource.Cluster, RbacOperation.View);
	if (!hasClusterAccess) {
		return <NoPermissionView entity="cluster" />;
	}
	return <Outlet />;
}

export const Route = createFileRoute("/workspace/cluster")({
	// The section has no page of its own: the bare path (old bookmarks, the
	// sidebar parent) lands on Overview. Children (/overview, /incidents) pass through.
	beforeLoad: ({ location }) => {
		if (location.pathname.replace(/\/+$/, "") === "/workspace/cluster") {
			throw redirect({ to: "/workspace/cluster/overview", search: location.search, replace: true });
		}
	},
	component: RouteComponent,
});