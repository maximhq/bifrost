import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AuditLogsPage from "./page";

function RouteComponent() {
	const hasAuditLogsAccess = useRbac(RbacResource.AuditLogs, RbacOperation.View);
	if (!hasAuditLogsAccess) {
		return <NoPermissionView entity="audit logs" />;
	}
	return <AuditLogsPage />;
}

export const Route = createFileRoute("/workspace/audit-logs")({
	component: RouteComponent,
});