import { createFileRoute } from "@tanstack/react-router";
import AgentGatewayPage from "./page";

export const Route = createFileRoute("/workspace/config/agent-gateway")({
	component: AgentGatewayPage,
});