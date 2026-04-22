import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import SCIMPage from "./page";

function RouteComponent() {
	const hasUserProvisioningAccess = useRbac(RbacResource.UserProvisioning, RbacOperation.View);
	if (!hasUserProvisioningAccess) {
		return <NoPermissionView entity="user provisioning" />;
	}
	return <SCIMPage />;
}

export const Route = createFileRoute("/workspace/scim")({
	component: RouteComponent,
});