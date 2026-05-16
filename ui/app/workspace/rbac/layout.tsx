import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import RBACRedirectPage from "./page";

function RouteComponent() {
	const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View);
	if (!hasRbacAccess) {
		return <NoPermissionView entity="roles and permissions" />;
	}
	return <RBACRedirectPage />;
}

export const Route = createFileRoute("/workspace/rbac")({
	component: RouteComponent,
});