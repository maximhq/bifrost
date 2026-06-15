import { createFileRoute, Outlet, useChildMatches, useLocation } from "@tanstack/react-router";
import FullPageLoader from "@/components/fullPageLoader";
import { NoPermissionView } from "@/components/noPermissionView";
import { useGetCoreConfigQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import ConfigPage from "./page";

function RouteComponent() {
	const pathname = useLocation({ select: (l) => l.pathname });
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	const hasAPIKeysAccess = useRbac(RbacResource.APIKeys, RbacOperation.View);
	const childMatches = useChildMatches();

	const isAPIKeysRoute = pathname.startsWith("/workspace/config/api-keys");
	const requiredAccess = isAPIKeysRoute ? hasAPIKeysAccess : hasSettingsAccess;

	const { isLoading } = useGetCoreConfigQuery({ fromDB: true }, { skip: !requiredAccess });

	if (!requiredAccess) {
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