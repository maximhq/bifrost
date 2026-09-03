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

import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { MCP_CREDENTIAL_STATUS_COLORS } from "@/lib/constants/config";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
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
import { Input } from "@/components/ui/input";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ChevronLeft, ChevronRight, Info, Search } from "lucide-react";
import { useToast } from "@/hooks/use-toast";
import { useT, type TranslateFn } from "@/lib/i18n";
import { getErrorMessage, useReauthMCPSessionMutation, useRevokeMCPSessionMutation } from "@/lib/store";
import { MCPSessionRow } from "@/lib/types/mcpSessions";
import { RefreshTokenStatus } from "@/components/refreshTokenStatus";
import { ScopeChips } from "@/components/scopeChips";
import { ExternalLink, Fingerprint, KeyRound, Loader2, MoreHorizontal, Pencil, RefreshCcw, Trash2, UserRound } from "lucide-react";
import { useState } from "react";

interface SessionsTableProps {
	sessions: MCPSessionRow[];
	totalCount: number;
	isFetching: boolean;
	search: string;
	onSearchChange: (value: string) => void;
	hasActiveFilters: boolean;
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
	hasActiveFilters,
	offset,
	limit,
	onOffsetChange,
}: SessionsTableProps) {
	const t = useT();
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
			toast({ title: t("mcp.sessions.reauthFailed"), description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const confirmRevoke = async () => {
		if (!pendingDelete) return;
		const row = pendingDelete;
		setPendingDelete(null);
		setPendingActionRowId(row.id);
		try {
			await revoke(row.id).unwrap();
			toast({ title: row.kind === "header" ? t("mcp.sessions.headerRevoked") : t("mcp.sessions.sessionRevoked") });
		} catch (err) {
			toast({
				title: row.kind === "header" ? t("mcp.sessions.headerRevokeFailed") : t("mcp.sessions.sessionRevokeFailed"),
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
								<AlertDialogTitle>{t("mcp.sessions.revokeHeadersTitle")}</AlertDialogTitle>
								<AlertDialogDescription>{t("mcp.sessions.revokeHeadersDesc")}</AlertDialogDescription>
							</>
						) : (
							<>
								<AlertDialogTitle>{t("mcp.sessions.revokeSessionTitle")}</AlertDialogTitle>
								<AlertDialogDescription>{t("mcp.sessions.revokeSessionDesc")}</AlertDialogDescription>
							</>
						)}
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="mcp-session-revoke-cancel">{t("common.cancel")}</AlertDialogCancel>
						<AlertDialogAction onClick={confirmRevoke} data-testid="mcp-session-revoke-confirm">
							{t("mcp.sessions.revoke")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<PageTitle title="MCP Auth Sessions">{t("mcp.sessions.pageDescription")}</PageTitle>

			<div className="mb-4 flex items-center gap-3">
				<div className="relative max-w-sm min-w-[200px] flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label={t("mcp.sessions.searchAria")}
						placeholder={t("mcp.sessions.searchPlaceholder")}
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="mcp-sessions-search-input"
					/>
				</div>
			</div>

			<div className="flex grow flex-col overflow-auto">
				<div className={`mb-2 grow overflow-auto rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
					<Table>
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead>{t("mcp.sessions.colServer")}</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("mcp.sessions.colType")} tooltip={t("mcp.sessions.typeTooltip")} />
								</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("mcp.sessions.colBoundTo")} tooltip={t("mcp.sessions.boundTooltip")} />
								</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("common.status")} tooltip={t("mcp.sessions.statusTooltip")} />
								</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("mcp.sessions.colScopes")} tooltip={t("mcp.sessions.scopesTooltip")} />
								</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("mcp.sessions.colExpiry")} tooltip={t("mcp.sessions.expiryTooltip")} />
								</TableHead>
								<TableHead>
									<HeaderWithTooltip label={t("mcp.sessions.colRefreshToken")} tooltip={t("mcp.sessions.refreshTokenTooltip")} />
								</TableHead>
								<TableHead>{t("mcp.sessions.colCreated")}</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-10 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{sessions.length === 0 ? (
								<TableRow>
									<TableCell colSpan={9} className="h-24 text-center">
										{hasActiveFilters ? (
											<div className="text-muted-foreground text-sm">{t("mcp.sessions.noMatch")}</div>
										) : (
											<span className="text-muted-foreground text-sm">{t("mcp.sessions.empty")}</span>
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
										<TableCell>
											<ScopesCell row={row} />
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">
											<div className="flex flex-col">
												<span>{formatAccessExpiry(row, t)}</span>
												{row.last_refreshed_at && (
													<span className="text-xs">
														{t("mcp.sessions.refreshedAgo", { time: formatRelativePast(row.last_refreshed_at, t) })}
													</span>
												)}
											</div>
										</TableCell>
										<TableCell className="text-sm">
											<RefreshTokenCell row={row} />
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{formatRelativePast(row.created_at, t)}</TableCell>
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
							{t("mcp.pagination.entries", {
								from: (offset + 1).toLocaleString(),
								to: Math.min(offset + limit, totalCount).toLocaleString(),
								total: totalCount.toLocaleString(),
							})}
						</div>

						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="mcp-sessions-pagination-prev-btn"
								aria-label={t("mcp.pagination.prev")}
							>
								<ChevronLeft className="size-3" />
							</Button>

							<div className="flex items-center gap-1">
								<span>{t("mcp.pagination.pageOf", { page: Math.floor(offset / limit) + 1, pages: Math.ceil(totalCount / limit) })}</span>
							</div>

							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="mcp-sessions-pagination-next-btn"
								aria-label={t("mcp.pagination.next")}
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
	const t = useT();
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
	return <span className="text-muted-foreground text-sm">{t("mcp.sessions.sessionBound")}</span>;
}

// Granted scopes on token rows. A dash when the provider reported none, and
// for header rows and pending sign-ins, which have no token.
function ScopesCell({ row }: { row: MCPSessionRow }) {
	if (row.kind !== "token" || !row.scopes?.length) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}
	return <ScopeChips scopes={row.scopes} />;
}

// Refresh token presence on token rows; a dash for header rows and pending
// sign-ins, which have no token.
function RefreshTokenCell({ row }: { row: MCPSessionRow }) {
	if (row.kind !== "token" || row.has_refresh_token === undefined) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}
	return <RefreshTokenStatus hasRefreshToken={row.has_refresh_token} status={row.status} />;
}

function TypeBadge({ authKind }: { authKind: string }) {
	const t = useT();
	if (authKind === "headers") {
		return <Badge variant="outline">{t("mcp.sessions.typeHeaders")}</Badge>;
	}
	return <Badge variant="outline">{t("mcp.sessions.typeOauth")}</Badge>;
}

// Colors come from the shared credential palette so a session's status reads
// the same as the credential block in the server sheet and the server state
// badge in the registry: green for usable, red for "a human must act", amber
// and gray for informational.
function StatusBadge({ status }: { status: string }) {
	const t = useT();
	const labels: Record<string, string> = {
		pending: t("mcp.sessions.pending"),
		orphaned: t("mcp.sessions.orphaned"),
		needs_reauth: t("mcp.sessions.needsReauth"),
		needs_update: t("mcp.sessions.needsUpdate"),
		active: t("mcp.sessions.active"),
	};
	return (
		<Badge className={MCP_CREDENTIAL_STATUS_COLORS[status] ?? MCP_CREDENTIAL_STATUS_COLORS.unknown}>
			{labels[status] ?? titleCaseFromSnakeCase(status)}
		</Badge>
	);
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
	const t = useT();
	const busy = reauthing || revoking;
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label={t("mcp.sessions.actionsAria")}
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
							{t("mcp.sessions.triggerReauth")}
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
							{t("mcp.sessions.completeAuth")}
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
								{row.status === "needs_update" ? t("mcp.sessions.updateValues") : t("mcp.sessions.editValues")}
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
							{t("mcp.sessions.revoke")}
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
								{t("mcp.sessions.reauthenticate")}
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
							{t("mcp.sessions.revoke")}
						</DropdownMenuItem>
					</>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function formatRelativePast(iso: string, t: TranslateFn): string {
	try {
		const ts = new Date(iso).getTime();
		if (Number.isNaN(ts)) return iso;
		const diffMs = Date.now() - ts;
		if (diffMs < 60_000) return t("mcp.sessions.justNow");
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 60) return t("mcp.sessions.minAgo", { count: mins });
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours < 48) return t("mcp.sessions.hoursAgo", { count: hours });
		const days = Math.floor(diffMs / 86_400_000);
		return t("mcp.sessions.daysAgo", { count: days });
	} catch {
		return iso;
	}
}

function formatAccessExpiry(row: MCPSessionRow, t: TranslateFn): string {
	// Header rows don't have an upstream-side expiry — the submitted values
	// are durable until the user revokes or the schema changes. The status
	// column already conveys lifecycle state (Active / Needs update /
	// Orphaned), so this column collapses to a dash for headers.
	if (row.kind === "header") {
		return "-";
	}
	if (!row.expires_at) return "-";
	try {
		const ts = new Date(row.expires_at).getTime();
		if (Number.isNaN(ts)) return row.expires_at;
		const diffMs = ts - Date.now();
		if (diffMs <= 0) {
			// A past expiry only reads as expired when nothing can renew the
			// token: needs_reauth, or no refresh token at all. With one, an
			// active row refreshes on next use, and an orphaned row still
			// holds a valid upstream credential that refreshes once access
			// is restored.
			if (row.has_refresh_token === false) return t("mcp.sessions.expired");
			switch (row.status) {
				case "active":
					return t("mcp.sessions.refreshesOnNextUse");
				case "orphaned":
					return t("mcp.sessions.refreshesWhenRestored");
				default:
					return t("mcp.sessions.expired");
			}
		}
		const days = Math.floor(diffMs / 86_400_000);
		if (days > 1) return t("mcp.sessions.inDays", { count: days });
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours > 1) return t("mcp.sessions.inHours", { count: hours });
		const mins = Math.floor(diffMs / 60_000);
		return t("mcp.sessions.inMin", { count: Math.max(mins, 1) });
	} catch {
		return row.expires_at;
	}
}