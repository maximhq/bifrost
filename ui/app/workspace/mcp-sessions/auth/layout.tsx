import { createFileRoute } from "@tanstack/react-router";
import MCPSessionsAuthPage from "./page";

function RouteComponent() {
	// Public-by-policy in OSS: the backend enforces identity match on the flow
	// row itself. We route any incoming caller to the page; if their identity
	// doesn't match the flow's, the API returns 403 and the page renders an
	// appropriate message.
	return <MCPSessionsAuthPage />;
}

export const Route = createFileRoute("/workspace/mcp-sessions/auth")({
	component: RouteComponent,
});
