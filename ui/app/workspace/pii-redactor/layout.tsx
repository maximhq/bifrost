import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import PiiRedactorPage from "./page";

function RouteComponent() {
	const hasPiiRedactorAccess = useRbac(RbacResource.PIIRedactor, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasPiiRedactorAccess) {
		return <NoPermissionView entity="PII redactor" />;
	}
	return childMatches.length === 0 ? <PiiRedactorPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/pii-redactor")({
	component: RouteComponent,
});