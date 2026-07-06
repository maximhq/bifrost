import { createFileRoute } from "@tanstack/react-router";
import MCPRegistryOAuthCallbackPage from "./page";

function RouteComponent() {
	// Public-by-policy — this is the landing page after the upstream OAuth
	// provider redirects the browser through /api/oauth/callback. The backend
	// has already done the token exchange; this page just closes the popup
	// (for admin-test OAuth on the MCP registry) or renders a fallback if
	// there's no opener.
	return <MCPRegistryOAuthCallbackPage />;
}

export const Route = createFileRoute("/workspace/mcp-registry/oauth-callback")({
	component: RouteComponent,
});