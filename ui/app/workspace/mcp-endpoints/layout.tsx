import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import EndpointsPage from "./page";

interface EndpointsSearch {
	create?: string; // namespace id to preselect in the create dialog
}

function RouteComponent() {
	const canView = useRbac(RbacResource.VirtualMCPs, RbacOperation.View);
	const search = Route.useSearch();
	if (!canView) {
		return <NoPermissionView entity="MCP endpoints" />;
	}
	return <EndpointsPage createFor={search.create} />;
}

export const Route = createFileRoute("/workspace/mcp-endpoints")({
	validateSearch: (search: Record<string, unknown>): EndpointsSearch => ({
		create: typeof search.create === "string" || typeof search.create === "number" ? String(search.create) : undefined,
	}),
	component: RouteComponent,
});
