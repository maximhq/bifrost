import { createFileRoute } from "@tanstack/react-router";
import MCPPortalPage from "./page";

export const Route = createFileRoute("/workspace/mcp-portal")({
	component: MCPPortalPage,
});