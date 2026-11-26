import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import MCPInspectorPage from "./page";

interface InspectorSearch {
	target?: string; // "endpoint:<name>" | "vmcp:<slug>" | "server:<slug>"
}

function RouteComponent() {
	const canView = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	const search = Route.useSearch();
	if (!canView) {
		return <NoPermissionView entity="MCP inspector" />;
	}
	return <MCPInspectorPage initialTarget={search.target} />;
}

export const Route = createFileRoute("/workspace/mcp-inspector")({
	validateSearch: (search: Record<string, unknown>): InspectorSearch => ({
		target: typeof search.target === "string" ? search.target : undefined,
	}),
	component: RouteComponent,
});
