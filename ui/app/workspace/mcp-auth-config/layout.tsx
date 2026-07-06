import { createFileRoute } from "@tanstack/react-router";
import MCPAuthConfigPage from "./page";

export const Route = createFileRoute("/workspace/mcp-auth-config")({
	component: MCPAuthConfigPage,
});