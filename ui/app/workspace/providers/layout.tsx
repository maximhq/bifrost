import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import Providers from "./page";

function RouteComponent() {
	const hasProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasProvidersAccess) {
		return <NoPermissionView entity="model providers" />;
	}
	return childMatches.length === 0 ? <Providers /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/providers")({
	component: RouteComponent,
});