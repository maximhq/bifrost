import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import WebhooksPage from "./page";

function RouteComponent() {
	const hasWebhooksAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const childMatches = useChildMatches();
	if (!hasWebhooksAccess) {
		return <NoPermissionView entity="webhooks" />;
	}
	// Render the endpoints list at the base path; defer to child routes (e.g. /deliveries).
	return childMatches.length === 0 ? <WebhooksPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/webhooks")({
	component: RouteComponent,
});