import { createFileRoute } from "@tanstack/react-router";
import MCPSessionsAuthFailedPage from "./page";

// Public landing for per-user OAuth callback failures. Symmetric to auth-success:
// the anonymous (temp-token) branch of the callback handler redirects here when
// upstream denied the request or the token exchange failed. publicShell makes
// it MinimalShell-only with no API calls — works without a dashboard cookie.
export const Route = createFileRoute("/workspace/mcp-sessions/auth-failed")({
	staticData: { publicShell: true },
	component: MCPSessionsAuthFailedPage,
});