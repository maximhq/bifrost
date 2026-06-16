import { createFileRoute } from "@tanstack/react-router";
import MCPToolGroupsPage from "./page";

export const Route = createFileRoute("/workspace/mcp-tool-groups")({
	component: MCPToolGroupsPage,
});