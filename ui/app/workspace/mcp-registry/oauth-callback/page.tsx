// Landing route for the upstream OAuth callback (admin-test OAuth flow path).
// Backend's /api/oauth/callback performs the actual token exchange, then
// 302s here with ?status=success or ?status=failed&error=... The popup
// opened by the MCP registry's OAuth2Authorizer expects a postMessage on
// its opener window; this page sends it and closes itself.
//
// If there's no opener (user opened the URL directly), fall back to the
// MCP registry — that's where the admin came from when triggering the
// admin-test flow.

import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";

export default function MCPRegistryOAuthCallbackPage() {
	const [closeAttempted, setCloseAttempted] = useState(false);

	useEffect(() => {
		const params = new URLSearchParams(window.location.search);
		const status = params.get("status");
		const error = params.get("error");

		if (window.opener) {
			if (status === "success") {
				window.opener.postMessage({ type: "oauth_success" }, window.location.origin);
			} else {
				window.opener.postMessage({ type: "oauth_failed", error: error ?? "OAuth flow failed" }, window.location.origin);
			}
			setCloseAttempted(true);
			window.close();
		}
	}, []);

	// If we got here, either there's no opener or the close call was blocked.
	// Render a small fallback so the tab isn't blank.
	const params = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : null;
	const status = params?.get("status") ?? "unknown";
	const error = params?.get("error");

	return (
		<div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-lg border p-8 text-center shadow-sm">
				<h1 className="text-xl font-semibold">{status === "success" ? "Authorization complete" : "Authorization failed"}</h1>
				{error && <p className="text-destructive mt-2 text-sm">{error}</p>}
				<p className="text-muted-foreground mt-4 text-sm">{closeAttempted ? "You can close this tab." : "This window can be closed."}</p>
				<div className="mt-6">
					<Button asChild variant="outline" data-testid="mcp-callback-back-button">
						<Link to="/workspace/mcp-registry">Back to MCP registry</Link>
					</Button>
				</div>
			</div>
		</div>
	);
}