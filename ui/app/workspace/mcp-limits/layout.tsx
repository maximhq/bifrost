import { createFileRoute } from "@tanstack/react-router";
import MCPLimitsPage from "./page";

function RouteComponent() {
	return <MCPLimitsPage />;
}

export const Route = createFileRoute("/workspace/mcp-limits")({
	component: RouteComponent,
});