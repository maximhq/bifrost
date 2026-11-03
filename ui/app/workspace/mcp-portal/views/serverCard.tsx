import { PageTagBadge } from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { getErrorMessage, useAuthenticateMCPPortalServerMutation } from "@/lib/store";
import type { MCPPortalCredentialStatus, MCPPortalServer } from "@/lib/types/mcpPortal";
import { ExternalLink, Loader2, Server } from "lucide-react";
import { useEffect, useRef, useState } from "react";

const credentialLabels: Record<MCPPortalCredentialStatus, string> = {
	ready: "Ready",
	missing: "Not configured",
	needs_reauth: "Reauthentication required",
	needs_update: "Update required",
	admin_managed: "Admin managed",
};

const authLabels: Record<string, string> = {
	none: "No authentication",
	headers: "Shared headers",
	oauth: "Shared OAuth",
	per_user_oauth: "Per-user OAuth",
	per_user_headers: "Per-user headers",
	token_exchange: "Token exchange",
};

export default function ServerCard({
	server,
	virtualKeyId,
	disabled,
}: {
	server: MCPPortalServer;
	virtualKeyId: string;
	disabled: boolean;
}) {
	const [authenticate, { isLoading }] = useAuthenticateMCPPortalServerMutation();
	const [actionError, setActionError] = useState<string | null>(null);
	const [opened, setOpened] = useState(false);
	const active = useRef(true);
	const pendingWindow = useRef<Window | null>(null);

	useEffect(() => {
		active.current = true;
		return () => {
			active.current = false;
			// Changing identity must never navigate an outstanding credential window.
			pendingWindow.current?.close();
			pendingWindow.current = null;
		};
	}, []);

	const configure = async () => {
		if (disabled || isLoading || pendingWindow.current || !server.can_configure) return;
		setActionError(null);
		setOpened(false);
		// Reserve the tab during the user gesture, before awaiting the API.
		const tab = window.open("about:blank", "_blank");
		if (!tab) {
			setActionError("Allow pop-ups for this site, then try again to open the credential page.");
			return;
		}
		tab.opener = null;
		pendingWindow.current = tab;
		try {
			const { auth_url } = await authenticate({
				id: server.id,
				...(virtualKeyId ? { virtual_key_id: virtualKeyId } : {}),
			}).unwrap();
			if (!active.current) return;
			const url = new URL(auth_url, window.location.origin);
			// Only the existing local credential landing may receive flow tokens.
			if (url.origin !== window.location.origin || url.pathname !== "/workspace/mcp-sessions/auth" || !url.searchParams.get("flow")) {
				throw new Error("The server returned an invalid credential page URL.");
			}
			if (tab.closed) throw new Error("The credential tab was closed. Try again to reopen it.");
			tab.location.replace(url.href);
			pendingWindow.current = null;
			setOpened(true);
		} catch (error) {
			tab.close();
			if (active.current) setActionError(error instanceof Error ? error.message : getErrorMessage(error));
		} finally {
			if (pendingWindow.current === tab) pendingWindow.current = null;
		}
	};

	const actionLabel =
		server.credential_status === "needs_reauth"
			? "Reauthenticate"
			: server.credential_status === "needs_update" || server.credential_status === "ready"
				? "Update credentials"
				: "Configure";

	return (
		<article className="bg-card flex flex-col rounded-lg border p-5" data-testid={`mcp-portal-server-${server.id}`}>
			<div className="mb-4 flex items-start gap-3">
				<div className="bg-muted rounded-md p-2">
					<Server className="text-muted-foreground size-5" />
				</div>
				<div className="min-w-0 flex-1">
					<h2 className="text-base font-semibold break-words">{server.name}</h2>
					<p className="text-muted-foreground mt-1 text-xs">
						{authLabels[server.auth_type] ?? (server.auth_type.replaceAll("_", " ") || "Unknown")}
					</p>
				</div>
			</div>
			{server.description && <p className="text-muted-foreground mb-4 text-sm break-words whitespace-pre-wrap">{server.description}</p>}
			<dl className="mb-4 space-y-3 text-sm">
				<div className="flex flex-wrap items-center justify-between gap-2">
					<dt className="text-muted-foreground">Credentials</dt>
					<dd data-testid={`mcp-portal-credential-status-${server.id}`}>
						<PageTagBadge
							tag={{
								label: credentialLabels[server.credential_status],
								tone:
									server.credential_status === "ready" ? "success" : server.credential_status === "admin_managed" ? "neutral" : "warning",
							}}
						/>
					</dd>
				</div>
				<div className="flex flex-wrap items-center justify-between gap-2">
					<dt className="text-muted-foreground">Connection</dt>
					<dd data-testid={`mcp-portal-connection-status-${server.id}`}>
						<PageTagBadge
							tag={{
								label: server.connection_state
									? server.connection_state.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase())
									: "Unknown",
								tone: server.connection_state === "healthy" ? "success" : server.connection_state === "error" ? "danger" : "neutral",
							}}
						/>
					</dd>
				</div>
			</dl>
			{!!server.required_header_keys?.length && (
				<div className="mb-4" data-testid={`mcp-portal-required-headers-${server.id}`}>
					<p className="text-muted-foreground mb-2 text-xs font-medium">Required headers</p>
					<div className="flex flex-wrap gap-1.5">
						{server.required_header_keys.map((header) => (
							<code key={header} className="bg-muted rounded px-2 py-1 text-xs break-all">
								{header}
							</code>
						))}
					</div>
				</div>
			)}
			<div className="mt-auto border-t pt-4">
				{server.can_configure ? (
					<>
						<Button
							variant="outline"
							className="w-full"
							onClick={configure}
							disabled={disabled || isLoading}
							data-testid={`mcp-portal-configure-${server.id}`}
						>
							{isLoading ? <Loader2 className="size-4 animate-spin" /> : <ExternalLink className="size-4" />}
							{isLoading ? "Opening…" : actionLabel}
						</Button>
						{opened && (
							<p className="text-muted-foreground mt-2 text-xs" role="status" data-testid={`mcp-portal-auth-opened-${server.id}`}>
								Complete authentication in the new tab, then return here and refresh.
							</p>
						)}
					</>
				) : (
					<p className="text-muted-foreground text-xs">
						{server.auth_type === "none" ? "No credentials needed." : "Managed by your administrator. No configuration needed here."}
					</p>
				)}
				{actionError && (
					<p className="text-destructive mt-2 text-sm" role="alert" data-testid={`mcp-portal-action-error-${server.id}`}>
						{actionError}
					</p>
				)}
			</div>
		</article>
	);
}