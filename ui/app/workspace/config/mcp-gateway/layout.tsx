import { createFileRoute } from "@tanstack/react-router";
import MCPGatewayPage from "./page";

export const Route = createFileRoute("/workspace/config/mcp-gateway")({
	component: MCPGatewayPage,
});