// Base sessions table: renders token rows + pending flow rows + per-user
// header credential rows visible to the caller's identity. VK-keyed rows
// render directly with their VK ID; user-keyed rows show the preloaded
// user.name (falling back to email, then raw user_id). The `user` field
// is populated server-side by the enterprise configstore wrapper; OSS
// leaves it absent and the UI falls back to the raw ID.
//
// Status badges:
//   active:       token / header row, usable
//   orphaned:     credential row (token or header); caller lost their last
//                 granting VK. Credential still intact — auto-reactivates
//                 when access is restored. Re-auth / edit wouldn't help so
//                 the corresponding action is hidden.
//   needs_reauth: token row; upstream credential dead (refresh failed).
//                 Re-auth required.
//   needs_update: header row; admin changed the PerUserHeaderKeys schema.
//                 Caller must resubmit values.
//   pending:      flow row, user must complete OAuth authentication.

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ChevronLeft, ChevronRight, Info } from "lucide-react";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useReauthMCPSessionMutation, useRevokeMCPSessionMutation } from "@/lib/store";
import { MCPSessionRow } from "@/lib/types/mcpSessions";
import { ExternalLink, Fingerprint, KeyRound, Loader2, MoreHorizontal, Pencil, RefreshCcw, Trash2, UserRound } from "lucide-react";
import { useState } from "react";
import SessionsFilterBar from "./sessionsFilterBar";

interface SessionsTableProps {
	sessions: MCPSessionRow[];
	totalCount: number;
	isFetching: boolean;
	search: string;
	onSearchChange: (value: string) => void;
	kindFilter: string[];
	onKindFilterChange: (value: string[]) => void;
	statusFilter: string[];
	onStatusFilterChange: (value: string[]) => void;
	authModeFilter: string[];
	onAuthModeFilterChange: (value: string[]) => void;
	hasActiveFilters: boolean;
	onClearFilters: () => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function SessionsTable({
	sessions,
	totalCount,
	isFetching,
	search,
	onSearchChange,
	kindFilter,
	onKindFilterChange,
	statusFilter,
	onStatusFilterChange,
	authModeFilter,
	onAuthModeFilterChange,
	hasActiveFilters,
	onClearFilters,
	offset,
	limit,
	onOffsetChange,
}: SessionsTableProps) {
	const { toast } = useToast();
	const [reauth, { isLoading: reauthing }] = useReauthMCPSessionMutation();
	const [revoke, { isLoading: revoking }] = useRevokeMCPSessionMutation();
	const [pendingDelete, setPendingDelete] = useState<MCPSessionRow | null>(null);
	const [pendingActionRowId, setPendingActionRowId] = useState<string | null>(null);

	const handleReauth = async (row: MCPSessionRow) => {
		setPendingActionRowId(row.id);
		try {
			const res = await reauth(row.id).unwrap();
			// Open the upstream authorize URL. User completes there, then
			// is redirected back to /api/oauth/callback by the provider.
			window.location.href = res.authorize_url;
		} catch (err) {
			setPendingActionRowId(null);
			toast({ title: "Re-authentication failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const confirmRevoke = async () => {
		if (!pendingDelete) return;
		const row = pendingDelete;
		setPendingDelete(null);
		setPendingActionRowId(row.id);
		try {
			await revoke(row.id).unwrap();
			toast({ title: row.kind === "header" ? "Header values revoked" : "Session revoked" });
		} catch (err) {
			toast({
				title: row.kind === "header" ? "Failed to revoke header values" : "Failed to revoke session",
				description: getErrorMessage(err),
				variant: "destructive",
			});
		} finally {
			setPendingActionRowId(null);
		}
	};

	return (
		<div className="flex grow flex-col overflow-auto">
			<AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						{pendingDelete?.kind === "header" ? (
							<>
								<AlertDialogTitle>Revoke these stored header values?</AlertDialogTitle>
								<AlertDialogDescription>
									Bifrost will remove the stored credential values for this binding. There is no upstream token to revoke; the user will
									need to resubmit their header values to use this MCP again.
								</AlertDialogDescription>
							</>
						) : (
							<>
								<AlertDialogTitle>Revoke this MCP session?</AlertDialogTitle>
								<AlertDialogDescription>
									Bifrost will remove the stored credential for this binding. The upstream OAuth token is not revoked at the provider; it
									stays detached and expires naturally. Anyone using this binding will need to re-authenticate to obtain a fresh token.
								</AlertDialogDescription>
							</>
						)}
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="mcp-session-revoke-cancel">Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={confirmRevoke} data-testid="mcp-session-revoke-confirm">
							Revoke
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">MCP Auth Sessions</h2>
					<p className="text-muted-foreground text-sm">
						Per-user credentials stored for MCP servers (OAuth tokens and submitted headers), plus any pending authentication flows.
					</p>
				</div>
			</div>

			<div className="mb-4">
				<SessionsFilterBar
					search={search}
					onSearchChange={onSearchChange}
					kindFilter={kindFilter}
					onKindFilterChange={onKindFilterChange}
					statusFilter={statusFilter}
					onStatusFilterChange={onStatusFilterChange}
					authModeFilter={authModeFilter}
					onAuthModeFilterChange={onAuthModeFilterChange}
					hasActiveFilters={hasActiveFilters}
					onClearFilters={onClearFilters}
				/>
			</div>

			<div className="flex grow flex-col overflow-auto">
				<div className={`mb-2 grow overflow-auto rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
					<Table>
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead>MCP server</TableHead>
								<TableHead>
									<HeaderWithTooltip
										label="Type"
										tooltip="OAuth: per-user OAuth credential, either a stored token from a completed sign-in, or a pending sign-in flow. Headers: per-user header values (API keys / signed tokens), either stored or pending submission."
									/>
								</TableHead>
								<TableHead>
									<HeaderWithTooltip
										label="Bound to"
										tooltip="The identity this credential is keyed to: an end user (via SSO), a virtual key (shared by anyone using that VK), or a client-issued session ID (asserted via the x-bf-mcp-session-id header)."
									/>
								</TableHead>
								<TableHead>
									<HeaderWithTooltip
										label="Status"
										tooltip="Active: credential valid and usable. Pending: OAuth flow in progress, user must complete sign-in. Needs re-auth: upstream credential expired or revoked at the provider; user must reconnect. Needs update: the admin changed the required header keys; user must resubmit. Orphaned: the user lost access to this MCP (e.g. an access profile change); credential is preserved and will become Active automatically if access is restored."
									/>
								</TableHead>
								<TableHead>
									<HeaderWithTooltip
										label="Access token expiry"
										tooltip="When the current access token expires. Bifrost auto-refreshes using the refresh token on the next request, so an active row past its expiry will silently mint a new token at use time. Header rows do not have an upstream expiry; their values stay valid until revoked or the schema changes."
									/>
								</TableHead>
								<TableHead>Created</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-10 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{sessions.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="h-24 text-center">
										{hasActiveFilters ? (
											<div className="text-muted-foreground text-sm">No sessions match these filters.</div>
										) : (
											<span className="text-muted-foreground text-sm">
												No sessions yet. Sessions appear here when an inference request or MCP gateway call triggers per-user authentication
												(OAuth or header submission).
											</span>
										)}
									</TableCell>
								</TableRow>
							) : (
								sessions.map((row) => (
									<TableRow key={`${row.kind}-${row.id}`} className="group">
										<TableCell className="font-medium">{row.mcp_client?.name || row.mcp_client?.client_id || "-"}</TableCell>
										<TableCell>
											<TypeBadge authKind={row.auth_kind} />
										</TableCell>
										<TableCell>
											<BindingCell row={row} />
										</TableCell>
										<TableCell>
											<StatusBadge status={row.status} />
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">
											<div className="flex flex-col">
												<span>{formatAccessExpiry(row)}</span>
												{row.last_refreshed_at && <span className="text-xs">refreshed {formatRelativePast(row.last_refreshed_at)}</span>}
											</div>
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{formatRelativePast(row.created_at)}</TableCell>
										<TableCell
											className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-10 bg-white text-right ${PIN_SHADOW_RIGHT}`}
										>
											<RowActions
												row={row}
												reauthing={reauthing}
												revoking={revoking}
												isPendingRow={pendingActionRowId === row.id}
												onReauth={() => handleReauth(row)}
												onRevoke={() => setPendingDelete(row)}
											/>
										</TableCell>
									</TableRow>
								))
							)}
						</TableBody>
					</Table>
				</div>

				{totalCount > 0 && (
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{(offset + 1).toLocaleString()}-{Math.min(offset + limit, totalCount).toLocaleString()} of {totalCount.toLocaleString()}{" "}
							entries
						</div>

						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="mcp-sessions-pagination-prev-btn"
								aria-label="Previous page"
							>
								<ChevronLeft className="size-3" />
							</Button>

							<div className="flex items-center gap-1">
								<span>Page</span>
								<span>{Math.floor(offset / limit) + 1}</span>
								<span>of {Math.ceil(totalCount / limit)}</span>
							</div>

							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="mcp-sessions-pagination-next-btn"
								aria-label="Next page"
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>
		</div>
	);
}

function HeaderWithTooltip({ label, tooltip }: { label: string; tooltip: string }) {
	return (
		<TooltipProvider delayDuration={150}>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex cursor-help items-center gap-2">
						{label}
						<Info className="text-muted-foreground size-3" />
					</span>
				</TooltipTrigger>
				<TooltipContent className="max-w-xs">{tooltip}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

function BindingCell({ row }: { row: MCPSessionRow }) {
	if (row.auth_mode === "user" && row.user_id) {
		const displayName = row.user?.name || row.user?.email;
		return (
			<div className="flex items-center gap-1.5 text-sm">
				<UserRound className="text-muted-foreground size-3.5" />
				{displayName ? <span>{displayName}</span> : <span className="font-mono">{row.user_id}</span>}
			</div>
		);
	}
	if (row.auth_mode === "vk" && row.virtual_key) {
		return (
			<div className="flex items-center gap-1.5 text-sm">
				<KeyRound className="text-muted-foreground size-3.5" />
				<span>{row.virtual_key.name || row.virtual_key.id}</span>
			</div>
		);
	}
	if (row.auth_mode === "session" && row.session_id) {
		return (
			<div className="flex items-center gap-1.5 text-sm">
				<Fingerprint className="text-muted-foreground size-3.5" />
				<span className="font-mono">{row.session_id}</span>
			</div>
		);
	}
	return <span className="text-muted-foreground text-sm">Session-bound</span>;
}

function TypeBadge({ authKind }: { authKind: string }) {
	if (authKind === "headers") {
		return <Badge variant="outline">Headers</Badge>;
	}
	return <Badge variant="outline">OAuth</Badge>;
}

function StatusBadge({ status }: { status: string }) {
	if (status === "pending") {
		return <Badge variant="secondary">Pending</Badge>;
	}
	if (status === "orphaned") {
		// Muted amber: distinct from destructive (red, action-required) and
		// secondary (gray, in-progress). Signals "informational, no action
		// needed from you" — the auto-restore cascade handles it.
		return (
			<Badge variant="outline" className="border-amber-500 bg-amber-100 text-amber-900 dark:bg-amber-900/30 dark:text-amber-200">
				Orphaned
			</Badge>
		);
	}
	if (status === "needs_reauth") {
		return <Badge variant="destructive">Needs re-auth</Badge>;
	}
	if (status === "needs_update") {
		// Outlined red: signals user action required, but visually distinct from
		// needs_reauth's solid destructive badge (which represents a hard auth failure).
		// Distinct copy so the row affordance ("Update values") matches.
		return (
			<Badge variant="outline" className="border-red-500 bg-red-100 text-red-900 dark:bg-red-900/30 dark:text-red-200">
				Needs update
			</Badge>
		);
	}
	return <Badge>Active</Badge>;
}

interface RowActionsProps {
	row: MCPSessionRow;
	reauthing: boolean;
	revoking: boolean;
	isPendingRow: boolean;
	onReauth: () => void;
	onRevoke: () => void;
}

function RowActions({ row, reauthing, revoking, isPendingRow, onReauth, onRevoke }: RowActionsProps) {
	const busy = reauthing || revoking;
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label="MCP session actions"
					data-testid={`mcp-session-row-actions-${row.id}`}
					disabled={busy}
				>
					{busy && isPendingRow ? <Loader2 className="h-4 w-4 animate-spin" /> : <MoreHorizontal className="h-4 w-4" />}
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				{row.kind === "flow" ? (
					row.status === "needs_reauth" ? (
						// The PKCE state on this flow row is dead; a fresh request to the
						// MCP client will start a new flow. No action we can offer wires
						// up to the existing flow row, so surface guidance instead.
						<DropdownMenuItem disabled className="text-muted-foreground cursor-default text-xs">
							Trigger a request to re-authenticate
						</DropdownMenuItem>
					) : (
						<DropdownMenuItem
							className="cursor-pointer"
							data-testid="mcp-session-complete-auth-menu-item"
							onSelect={(e) => {
								e.preventDefault();
								// Header flows need &kind=headers so the auth landing page
								// routes to the per-user-headers backend; OAuth flows use
								// the default branch.
								const url =
									row.auth_kind === "headers"
										? `/workspace/mcp-sessions/auth?flow=${row.id}&kind=headers`
										: `/workspace/mcp-sessions/auth?flow=${row.id}`;
								window.location.href = url;
							}}
						>
							<ExternalLink className="h-4 w-4" />
							Complete authentication
						</DropdownMenuItem>
					)
				) : row.kind === "header" ? (
					<>
						{row.status !== "orphaned" && row.can_reauth && (
							// "Edit values" hits reauth server-side: the handler mints a
							// fresh header submission flow + temp token and returns the
							// auth-landing URL. Same single-click → redirect dance as the
							// OAuth row's "Re-authenticate" action. Hidden when can_reauth
							// is false — user-bound credentials are only resubmittable by
							// the bound user (server enforces with 403).
							<DropdownMenuItem
								className="cursor-pointer"
								disabled={busy}
								data-testid="mcp-session-edit-headers-menu-item"
								onSelect={(e) => {
									e.preventDefault();
									onReauth();
								}}
							>
								<Pencil className="h-4 w-4" />
								{row.status === "needs_update" ? "Update values" : "Edit values"}
							</DropdownMenuItem>
						)}
						<DropdownMenuItem
							variant="destructive"
							className="cursor-pointer"
							disabled={busy}
							data-testid="mcp-session-revoke-menu-item"
							onSelect={(e) => {
								e.preventDefault();
								onRevoke();
							}}
						>
							<Trash2 className="h-4 w-4" />
							Revoke
						</DropdownMenuItem>
					</>
				) : (
					<>
						{row.status !== "orphaned" && row.can_reauth && (
							// Re-auth on an orphaned row wouldn't help: the upstream
							// credential is intact, the user just no longer has any
							// granting VK. Surface guidance instead of an action.
							// Hidden when can_reauth is false — user-bound rows are only
							// reauthable by the bound user (server enforces with 403).
							<DropdownMenuItem
								className="cursor-pointer"
								disabled={busy}
								data-testid="mcp-session-reauth-menu-item"
								onSelect={(e) => {
									e.preventDefault();
									onReauth();
								}}
							>
								<RefreshCcw className="h-4 w-4" />
								Re-authenticate
							</DropdownMenuItem>
						)}
						<DropdownMenuItem
							variant="destructive"
							className="cursor-pointer"
							disabled={busy}
							data-testid="mcp-session-revoke-menu-item"
							onSelect={(e) => {
								e.preventDefault();
								onRevoke();
							}}
						>
							<Trash2 className="h-4 w-4" />
							Revoke
						</DropdownMenuItem>
					</>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function formatRelativePast(iso: string): string {
	try {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t)) return iso;
		const diffMs = Date.now() - t;
		if (diffMs < 60_000) return "just now";
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 60) return `${mins} min ago`;
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours < 48) return `${hours}h ago`;
		const days = Math.floor(diffMs / 86_400_000);
		return `${days}d ago`;
	} catch {
		return iso;
	}
}

function formatAccessExpiry(row: MCPSessionRow): string {
	// Header rows don't have an upstream-side expiry — the submitted values
	// are durable until the user revokes or the schema changes. The status
	// column already conveys lifecycle state (Active / Needs update /
	// Orphaned), so this column collapses to a dash for headers.
	if (row.kind === "header") {
		return "-";
	}
	if (!row.expires_at) return "-";
	try {
		const t = new Date(row.expires_at).getTime();
		if (Number.isNaN(t)) return row.expires_at;
		const diffMs = t - Date.now();
		if (diffMs < 0) {
			// Active rows auto-refresh on next use via the refresh token.
			// Orphaned rows still hold a valid upstream credential — the past
			// expiry just means the cached access token is stale; if access
			// is restored, refresh kicks in. Only 'needs_reauth' is genuinely
			// expired (the refresh token itself is dead).
			switch (row.status) {
				case "active":
					return "Refreshes on next use";
				case "orphaned":
					return "Refreshes when access is restored";
				default:
					return "expired";
			}
		}
		const days = Math.floor(diffMs / 86_400_000);
		if (days > 1) return `in ${days} days`;
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours > 1) return `in ${hours} hours`;
		const mins = Math.floor(diffMs / 60_000);
		return `in ${Math.max(mins, 1)} min`;
	} catch {
		return row.expires_at;
	}
}