import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useTranslation } from "react-i18next";
import VirtualMCPsPage from "./page";

function RouteComponent() {
	const { t } = useTranslation("mcp");
	const hasVirtualMCPsAccess = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	if (!hasVirtualMCPsAccess) {
		return <NoPermissionView entity={t("virtualMcps.title")} />;
	}
	return <VirtualMCPsPage />;
}

export const Route = createFileRoute("/workspace/virtual-mcps")({
	component: RouteComponent,
});
