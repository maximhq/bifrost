import { createFileRoute } from "@tanstack/react-router";
import MCPLogsPage from "./page";

export const Route = createFileRoute("/workspace/mcp-logs")({
	component: MCPLogsPage,
});