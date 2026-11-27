import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import RelaysPage from "./page";

// Relays are gated on their own Relays resource, independent of the provider permission.
function RouteComponent() {
	const hasAccess = useRbac(RbacResource.Relays, RbacOperation.View);
	if (!hasAccess) {
		return <NoPermissionView entity="relays" />;
	}
	return <RelaysPage />;
}

export const Route = createFileRoute("/workspace/relays")({
	component: RouteComponent,
});