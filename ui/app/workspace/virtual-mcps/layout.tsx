import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import VirtualMCPsPage from "./page";

function RouteComponent() {
	const hasVirtualMCPsAccess = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	if (!hasVirtualMCPsAccess) {
		return <NoPermissionView entity="Virtual MCPs" />;
	}
	return <VirtualMCPsPage />;
}

export const Route = createFileRoute("/workspace/virtual-mcps")({
	component: RouteComponent,
});
