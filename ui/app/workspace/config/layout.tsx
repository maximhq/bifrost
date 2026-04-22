import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import FullPageLoader from "@/components/fullPageLoader";
import { NoPermissionView } from "@/components/noPermissionView";
import { useGetCoreConfigQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import ConfigPage from "./page";

function RouteComponent() {
	const hasConfigAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	const { isLoading } = useGetCoreConfigQuery({ fromDB: true }, { skip: !hasConfigAccess });
	const childMatches = useChildMatches();

	if (!hasConfigAccess) {
		return <NoPermissionView entity="configuration" />;
	}

	if (isLoading) {
		return <FullPageLoader />;
	}

	return childMatches.length === 0 ? <ConfigPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/config")({
	component: RouteComponent,
});