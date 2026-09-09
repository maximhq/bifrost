import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import WebhookDeliveriesPage from "./page";

function RouteComponent() {
	const hasWebhooksAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	if (!hasWebhooksAccess) {
		return <NoPermissionView entity="webhooks" />;
	}
	return <WebhookDeliveriesPage />;
}

export const Route = createFileRoute("/workspace/webhooks/deliveries")({
	component: RouteComponent,
});