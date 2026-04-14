import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import VirtualKeysRedirectPage from "./page";

function RouteComponent() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	if (!hasVirtualKeysAccess) {
		return <NoPermissionView entity="virtual keys" />;
	}
	return <VirtualKeysRedirectPage />;
}

export const Route = createFileRoute("/workspace/virtual-keys")({
	component: RouteComponent,
});