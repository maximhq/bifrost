import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import CustomPricingPage from "./page";

function CustomPricingLayout({ children }: { children: React.ReactNode }) {
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	if (!hasSettingsAccess) {
		return <NoPermissionView entity="custom pricing" />;
	}
	return <>{children}</>;
}

function RouteComponent() {
	const childMatches = useChildMatches();
	return <CustomPricingLayout>{childMatches.length === 0 ? <CustomPricingPage /> : <Outlet />}</CustomPricingLayout>;
}

export const Route = createFileRoute("/workspace/custom-pricing")({
	component: RouteComponent,
});