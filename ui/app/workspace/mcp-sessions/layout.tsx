import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import MCPSessionsPage from "./page";

function RouteComponent() {
	// Per-user OAuth sessions are scoped to the caller's own identity on the
	// backend, so any authenticated dashboard user can view their tab. No RBAC
	// resource yet — enterprise can layer DAC scoping on top of the API.
	//
	// Yield to child routes (/auth, /oauth-callback) when one matches;
	// otherwise render the list view as the index of this section.
	const childMatches = useChildMatches();
	return childMatches.length === 0 ? <MCPSessionsPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/mcp-sessions")({
	component: RouteComponent,
});