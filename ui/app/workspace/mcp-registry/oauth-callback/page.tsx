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
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, CircleCheck, CircleX } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";

export default function MCPRegistryOAuthCallbackPage() {
	const { t } = useTranslation("mcp");
	const [closeAttempted, setCloseAttempted] = useState(false);

	useEffect(() => {
		const params = new URLSearchParams(window.location.search);
		const status = params.get("status");
		const error = params.get("error");

		if (window.opener) {
			if (status === "success") {
				window.opener.postMessage({ type: "oauth_success" }, window.location.origin);
			} else {
				window.opener.postMessage(
					{ type: "oauth_failed", error: error ?? i18n.t("registry.authorizer.oauth.flowFailed", { ns: "mcp" }) },
					window.location.origin,
				);
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

	const isSuccess = status === "success";
	const StatusIcon = isSuccess ? CircleCheck : CircleX;

	return (
		<div className="flex min-h-[60vh] items-center justify-center p-4 sm:p-6" data-testid="mcp-callback">
			<section className="bg-card w-full max-w-lg rounded-sm border p-6 sm:p-8" aria-labelledby="mcp-callback-title">
				<div
					className={cn(
						"flex size-11 items-center justify-center rounded-sm",
						isSuccess ? "bg-primary/10 text-primary" : "bg-destructive/10 text-destructive",
					)}
				>
					<StatusIcon className="size-5" aria-hidden="true" />
				</div>
				<h1 id="mcp-callback-title" className="text-foreground mt-5 text-xl font-semibold tracking-tight">
					{isSuccess ? t("oauthCallback.successTitle") : t("oauthCallback.failedTitle")}
				</h1>
				<p className="text-muted-foreground mt-2 max-w-md text-sm leading-6">
					{isSuccess ? t("oauthCallback.successDescription") : t("oauthCallback.failedDescription")}
				</p>
				{error && (
					<p
						className="bg-destructive/10 text-destructive border-destructive/20 mt-4 rounded-sm border px-3 py-2 text-xs break-words"
						data-testid="mcp-callback-error"
					>
						{error}
					</p>
				)}
				<div className="mt-6 flex items-center gap-3 border-t pt-5">
					<Button asChild size="sm" variant="outline" data-testid="mcp-callback-back-button">
						<Link to="/workspace/mcp-registry">
							<ArrowLeft aria-hidden="true" />
							{t("oauthCallback.backToRegistry")}
						</Link>
					</Button>
					<p className="text-muted-foreground text-xs">{closeAttempted ? t("oauthCallback.closeTab") : t("oauthCallback.closeWindow")}</p>
				</div>
			</section>
		</div>
	);
}