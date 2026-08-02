import { createFileRoute } from "@tanstack/react-router";
import MCPSessionsAuthSuccessPage from "./page";

// Landing page shown after a per-user OAuth callback completes successfully.
// `publicShell` tells ClientLayout to render the MinimalShell unconditionally
// — the post-OAuth redirect arrives with no fragment and no dashboard cookie
// (temp-token visitors authenticated externally, not against Bifrost), so the
// normal tempTokenScoped logic wouldn't fire. This flag short-circuits all of
// that: no chrome, no auth probe, no API calls, just a static "done" view.
export const Route = createFileRoute("/workspace/mcp-sessions/auth-success")({
	staticData: { publicShell: true },
	component: MCPSessionsAuthSuccessPage,
});