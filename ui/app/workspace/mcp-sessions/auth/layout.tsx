import TempTokenScope from "@/components/tempTokenScope";
import { createFileRoute } from "@tanstack/react-router";
import MCPSessionsAuthPage from "./page";

// staticData.tempTokenScoped opts this route out of the dashboard chrome —
// ClientLayout renders a minimal shell and skips the protected
// useGetCoreConfigQuery fetch when this flag is set, so an unauthenticated
// browser can land on this page without bouncing to /login.
//
// TempTokenScope handles the auth half: it reads the `#t=…` fragment the
// server appended to the URL, attaches it as `X-Bifrost-Temp-Token` on
// outbound API calls, and suppresses the global 401-redirect so a stale
// link renders an inline error instead.
function RouteComponent() {
	return (
		<TempTokenScope name="mcp_auth">
			<MCPSessionsAuthPage />
		</TempTokenScope>
	);
}

export const Route = createFileRoute("/workspace/mcp-sessions/auth")({
	staticData: { tempTokenScoped: true },
	component: RouteComponent,
});