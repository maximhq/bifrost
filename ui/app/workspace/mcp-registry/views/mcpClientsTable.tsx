import ClientForm from "@/app/workspace/mcp-registry/views/mcpClientForm";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import {
	getErrorMessage,
	useDeleteMCPClientMutation,
	useInitiateMCPClientVerificationMutation,
	useReauthorizeMCPClientMutation,
	useReconnectMCPClientMutation,
	useUpdateMCPClientMutation,
	useVerifyMCPClientExchangeMutation,
	useVerifyMCPClientHeadersMutation,
} from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import {
	Box,
	ChevronLeft,
	ChevronRight,
	Info,
	KeyRound,
	Loader2,
	MoreHorizontal,
	PencilIcon,
	Plus,
	RefreshCcw,
	Search,
	Trash2,
	X,
} from "lucide-react";
import { ReactNode, useEffect, useMemo, useState } from "react";
import { IconWrap, InfoBox } from "./authorizerUi";
import MCPClientSheet from "./mcpClientSheet";
import { canReconnectMCPClient } from "./mcpClientsTable.utils";
import { MCPHeadersAuthorizer } from "./mcpHeadersAuthorizer";
import { MCPServersEmptyState } from "./mcpServersEmptyState";
import { MCPUsageGuideSheet } from "./mcpUsageGuide";
import { OAuth2Authorizer } from "./oauth2Authorizer";

function MCPClientActionsMenu({
	client,
	hasUpdateAccess,
	hasDeleteAccess,
	isReconnecting,
	isAuthorizing,
	isReauthorizing,
	isVerifyingExchange,
	canReconnect,
	onEdit,
	onReconnect,
	onAuthorize,
	onReauthorize,
	onRefreshHeaders,
	onVerifyExchange,
	onDelete,
}: {
	client: MCPClient;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	isReconnecting: boolean;
	isAuthorizing: boolean;
	isReauthorizing: boolean;
	isVerifyingExchange: boolean;
	canReconnect: boolean;
	onEdit: (client: MCPClient) => void;
	onReconnect: (client: MCPClient) => void;
	onAuthorize: (client: MCPClient) => void;
	onReauthorize: (client: MCPClient) => void;
	onRefreshHeaders: (client: MCPClient) => void;
	onVerifyExchange: (client: MCPClient) => void;
	onDelete: (client: MCPClient) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label="MCP server actions"
					data-testid={`mcp-client-actions-${client.config.client_id}-btn`}
					disabled={isReconnecting || isReauthorizing || isVerifyingExchange}
				>
					{isReconnecting || isAuthorizing || isReauthorizing || isVerifyingExchange ? (
						<Loader2 className="h-4 w-4 animate-spin" />
					) : (
						<MoreHorizontal className="h-4 w-4" />
					)}
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="end"
				onCloseAutoFocus={(e) => {
					// Edit opens a Sheet; letting the dropdown restore focus to its
					// trigger fights the Sheet's autofocus and leaves focus outside
					// the dialog — which breaks ESC-to-close. Hand focus off to the
					// Sheet by skipping the dropdown's auto-restore.
					e.preventDefault();
				}}
			>
				{hasUpdateAccess && (
					<DropdownMenuItem
						className="cursor-pointer"
						data-testid={`mcp-client-edit-${client.config.client_id}-menu-item`}
						onSelect={(e) => {
							e.preventDefault();
							onEdit(client);
							setIsOpen(false);
						}}
					>
						<PencilIcon className="h-4 w-4" />
						Edit
					</DropdownMenuItem>
				)}
				{hasUpdateAccess && client.state === "pending_verification" && (
					<DropdownMenuItem
						className="cursor-pointer"
						disabled={isAuthorizing}
						data-testid={`mcp-client-authorize-${client.config.client_id}-menu-item`}
						onSelect={(e) => {
							e.preventDefault();
							onAuthorize(client);
							setIsOpen(false);
						}}
					>
						<KeyRound className="h-4 w-4" />
						Authorize
					</DropdownMenuItem>
				)}
				{hasUpdateAccess && canReconnect && (
					<DropdownMenuItem
						className="cursor-pointer"
						disabled={client.config.disabled || isReconnecting || client.state === "pending_verification" || client.state === "needs_reauth"}
						onSelect={(e) => {
							e.preventDefault();
							onReconnect(client);
							setIsOpen(false);
						}}
					>
						<RefreshCcw className="h-4 w-4" />
						Reconnect
					</DropdownMenuItem>
				)}
				{hasUpdateAccess &&
					client.state !== "pending_verification" &&
					client.state !== "disabled" &&
					(client.config.auth_type === "oauth" || client.config.auth_type === "per_user_oauth") && (
						<DropdownMenuItem
							className="cursor-pointer"
							disabled={isReauthorizing}
							data-testid={`mcp-client-reauthorize-${client.config.client_id}-menu-item`}
							onSelect={(e) => {
								e.preventDefault();
								onReauthorize(client);
								setIsOpen(false);
							}}
						>
							<KeyRound className="h-4 w-4" />
							{client.config.auth_type === "per_user_oauth" ? "Refresh admin credential" : "Reauthorize"}
						</DropdownMenuItem>
					)}
				{hasUpdateAccess &&
					client.state !== "pending_verification" &&
					client.state !== "disabled" &&
					client.config.auth_type === "per_user_headers" && (
						<DropdownMenuItem
							className="cursor-pointer"
							data-testid={`mcp-client-refresh-headers-${client.config.client_id}-menu-item`}
							onSelect={(e) => {
								e.preventDefault();
								onRefreshHeaders(client);
								setIsOpen(false);
							}}
						>
							<KeyRound className="h-4 w-4" />
							Refresh admin credential
						</DropdownMenuItem>
					)}
				{hasUpdateAccess &&
					client.state !== "pending_verification" &&
					client.state !== "disabled" &&
					client.config.auth_type === "token_exchange" && (
						<DropdownMenuItem
							className="cursor-pointer"
							disabled={isVerifyingExchange}
							data-testid={`mcp-client-verify-exchange-${client.config.client_id}-menu-item`}
							onSelect={(e) => {
								e.preventDefault();
								onVerifyExchange(client);
								setIsOpen(false);
							}}
						>
							<KeyRound className="h-4 w-4" />
							Re-verify as me
						</DropdownMenuItem>
					)}
				{hasDeleteAccess && (
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						onSelect={(e) => {
							e.preventDefault();
							onDelete(client);
							setIsOpen(false);
						}}
					>
						<Trash2 className="h-4 w-4" />
						Delete
					</DropdownMenuItem>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface MCPClientsTableProps {
	mcpClients: MCPClient[];
	totalCount: number;
	refetch?: () => void;
	search: string;
	debouncedSearch: string;
	server: string;
	/** Whether any sidebar facet filter (connection/auth/code-mode/status) is active. */
	filtersActive?: boolean;
	onSearchChange: (value: string) => void;
	onServerFilterClear: () => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function MCPClientsTable({
	mcpClients,
	totalCount,
	refetch,
	search,
	debouncedSearch,
	server,
	filtersActive = false,
	onSearchChange,
	onServerFilterClear,
	offset,
	limit,
	onOffsetChange,
}: MCPClientsTableProps) {
	const [formOpen, setFormOpen] = useState(false);
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasDeleteMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Delete);
	const [selectedMCPClient, setSelectedMCPClient] = useState<MCPClient | null>(null);
	const [clientToDelete, setClientToDelete] = useState<MCPClient | null>(null);
	// Drives the token_exchange "Re-verify as me" confirm dialog. Unlike
	// per_user_oauth/per_user_headers, verify-exchange needs no popup or
	// sample values — it fires as soon as confirmed — but admins still need
	// the same "what does this touch, when do I need it" context those flows
	// get from OAuth2Authorizer/MCPHeadersAuthorizer before we exchange their
	// identity token.
	const [exchangeVerifyClient, setExchangeVerifyClient] = useState<MCPClient | null>(null);
	const [showDetailSheet, setShowDetailSheet] = useState(false);
	const { toast } = useToast();

	const [reconnectingClients, setReconnectingClients] = useState<string[]>([]);
	const [authorizingClients, setAuthorizingClients] = useState<string[]>([]);
	const [reauthorizingClients, setReauthorizingClients] = useState<string[]>([]);
	const [verifyingExchangeClients, setVerifyingExchangeClients] = useState<string[]>([]);
	const [togglingClientIds, setTogglingClientIds] = useState<Set<string>>(new Set());
	// Drives the OAuth2Authorizer dialog for a config.json-bootstrapped client
	// sitting in pending_verification, triggered from the row actions menu.
	const [bootstrapAuthorize, setBootstrapAuthorize] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth: boolean;
	} | null>(null);
	// Drives the MCPHeadersAuthorizer dialog for a config.json-bootstrapped
	// per_user_headers client sitting in pending_verification, triggered from
	// the row actions menu.
	const [bootstrapHeadersClient, setBootstrapHeadersClient] = useState<MCPClient | null>(null);
	// Drives the OAuth2Authorizer dialog for a client redoing consent via
	// POST /reauthorize, triggered from the row actions menu.
	const [reauthorizeFlow, setReauthorizeFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth: boolean;
	} | null>(null);
	// Drives the MCPHeadersAuthorizer dialog for a per_user_headers client
	// refreshing its admin discovery credential, triggered from the row
	// actions menu (mirrors reauthorizeFlow above for per_user_oauth).
	const [headersRefreshFlow, setHeadersRefreshFlow] = useState<{
		mcpClientId: string;
		perUserHeaderKeys: string[];
	} | null>(null);

	// RTK Query mutations
	const [reconnectMCPClient] = useReconnectMCPClientMutation();
	const [reauthorizeMCPClient] = useReauthorizeMCPClientMutation();
	const [verifyMCPClientExchange] = useVerifyMCPClientExchangeMutation();
	const [deleteMCPClient] = useDeleteMCPClientMutation();
	const [updateMCPClient] = useUpdateMCPClientMutation();
	const [initiateVerification] = useInitiateMCPClientVerificationMutation();
	const [verifyMCPClientHeaders] = useVerifyMCPClientHeadersMutation();

	const handleCreate = () => {
		setFormOpen(true);
	};

	const handleReconnect = async (client: MCPClient) => {
		try {
			setReconnectingClients((prev) => [...prev, client.config.client_id]);
			await reconnectMCPClient(client.config.client_id).unwrap();
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.client_id));
			toast({ title: "Reconnected", description: `Client ${client.config.name} reconnected successfully.` });
			if (refetch) {
				await refetch();
			}
		} catch (error) {
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.client_id));
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleStartBootstrap = async (client: MCPClient) => {
		// per_user_headers takes a synchronous form-based path, token_exchange
		// opens the same "Re-verify as me" confirm dialog used to repair an
		// already-verified client; OAuth-based types kick off the existing
		// browser flow below.
		if (client.config.auth_type === "per_user_headers") {
			setBootstrapHeadersClient(client);
			return;
		}
		if (client.config.auth_type === "token_exchange") {
			handleRequestVerifyExchange(client);
			return;
		}
		const isPerUserOauth = client.config.auth_type === "per_user_oauth";
		// OAuth2Authorizer always shows a confirm step first and opens its own
		// popup synchronously from that step's own "Continue" click, for both
		// OAuth flavors — nothing to pre-open here. (This used to pre-open a
		// blank popup for the shared-oauth case, based on a stale assumption
		// that only per_user_oauth showed a confirm step; that left a stray
		// blank tab open alongside the dialog on every shared-OAuth authorize.)
		try {
			setAuthorizingClients((prev) => [...prev, client.config.client_id]);
			const response = await initiateVerification(client.config.client_id).unwrap();
			if (response.status === "pending_oauth" && response.authorize_url) {
				setBootstrapAuthorize({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: client.config.client_id,
					isPerUserOauth,
				});
			} else {
				toast({
					title: "Authorization failed",
					description: "Unexpected response from server. Please try again.",
					variant: "destructive",
				});
			}
		} catch (error) {
			toast({ title: "Authorization failed", description: getErrorMessage(error), variant: "destructive" });
		} finally {
			setAuthorizingClients((prev) => prev.filter((id) => id !== client.config.client_id));
		}
	};

	const handleReauthorize = async (client: MCPClient) => {
		try {
			setReauthorizingClients((prev) => [...prev, client.config.client_id]);
			const response = await reauthorizeMCPClient(client.config.client_id).unwrap();
			if (response.status === "pending_oauth" && response.authorize_url) {
				setReauthorizeFlow({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: client.config.client_id,
					isPerUserOauth: client.config.auth_type === "per_user_oauth",
				});
			} else {
				toast({
					title: "Reauthorization failed",
					description: "Unexpected response from server. Please try again.",
					variant: "destructive",
				});
			}
		} catch (error) {
			toast({ title: "Reauthorization failed", description: getErrorMessage(error), variant: "destructive" });
		} finally {
			setReauthorizingClients((prev) => prev.filter((id) => id !== client.config.client_id));
		}
	};

	// Opens the MCPHeadersAuthorizer to refresh a per_user_headers client's
	// admin discovery credential. Unlike OAuth's reauthorize, there's no
	// server round-trip to kick off first — the dialog collects sample values
	// itself and posts them directly to verify-headers.
	const handleRefreshHeaders = (client: MCPClient) => {
		setHeadersRefreshFlow({
			mcpClientId: client.config.client_id,
			perUserHeaderKeys: client.config.per_user_header_keys ?? [],
		});
	};

	// Opens the "Re-verify as me" confirm dialog for a token_exchange client.
	// The exchange call itself is synchronous and inputless — the backend
	// exchanges the signed-in admin's own identity token, so there's nothing
	// to collect — but admins still need the same "what does this touch, when
	// do I need it" context OAuth2Authorizer/MCPHeadersAuthorizer give before
	// their identity token gets used.
	const handleRequestVerifyExchange = (client: MCPClient) => {
		setExchangeVerifyClient(client);
	};

	const handleVerifyExchange = async (client: MCPClient) => {
		try {
			setVerifyingExchangeClients((prev) => [...prev, client.config.client_id]);
			const response = await verifyMCPClientExchange(client.config.client_id).unwrap();
			toast({ title: "Verified", description: response.message });
			if (refetch) {
				await refetch();
			}
		} catch (error) {
			toast({ title: "Verification failed", description: getErrorMessage(error), variant: "destructive" });
		} finally {
			setVerifyingExchangeClients((prev) => prev.filter((id) => id !== client.config.client_id));
		}
	};

	const handleDelete = async (client: MCPClient) => {
		try {
			await deleteMCPClient(client.config.client_id).unwrap();
			toast({ title: "Deleted", description: `Client ${client.config.name} removed successfully.` });
			if (refetch) {
				await refetch();
			}
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleSaved = async () => {
		setFormOpen(false);
		if (refetch) {
			await refetch();
		}
	};

	const getConnectionTypeDisplay = (type: string) => {
		switch (type) {
			case "http":
				return "HTTP";
			case "sse":
				return "SSE";
			case "stdio":
				return "STDIO";
			default:
				return type.toUpperCase();
		}
	};

	const getAuthTypeDisplay = (type: string | undefined) => {
		switch (type) {
			case "none":
			case undefined:
			case "":
				return "None";
			case "headers":
			case "per_user_headers":
				return "Headers";
			case "oauth":
			case "per_user_oauth":
				return "OAuth";
			case "token_exchange":
				return "Token Exchange";
			default:
				return type;
		}
	};

	const getAuthScopeDisplay = (type: string | undefined) => {
		switch (type) {
			case "per_user_oauth":
			case "per_user_headers":
			case "token_exchange":
				return "Per-User";
			case "oauth":
			case "headers":
				return "Shared";
			default:
				return "-";
		}
	};

	const handleRowClick = (mcpClient: MCPClient) => {
		setSelectedMCPClient(mcpClient);
		setShowDetailSheet(true);
	};

	const handleDetailSheetClose = () => {
		setShowDetailSheet(false);
		setSelectedMCPClient(null);
	};

	const selectedMCPClientIndex = useMemo(
		() => (selectedMCPClient ? mcpClients.findIndex((c) => c.config.client_id === selectedMCPClient.config.client_id) : -1),
		[selectedMCPClient, mcpClients],
	);

	const [pendingEdgeNav, setPendingEdgeNav] = useState<"first" | "last" | null>(null);

	useEffect(() => {
		if (pendingEdgeNav && mcpClients.length > 0) {
			const target = pendingEdgeNav === "first" ? mcpClients[0] : mcpClients[mcpClients.length - 1];
			setSelectedMCPClient(target);
			setPendingEdgeNav(null);
		}
	}, [pendingEdgeNav, mcpClients]);

	const handleDetailNavigate = (direction: "prev" | "next") => {
		const newIndex = direction === "prev" ? selectedMCPClientIndex - 1 : selectedMCPClientIndex + 1;
		if (newIndex >= 0 && newIndex < mcpClients.length) {
			setSelectedMCPClient(mcpClients[newIndex]);
		} else if (direction === "next" && offset + limit < totalCount) {
			onOffsetChange(offset + limit);
			setPendingEdgeNav("first");
		} else if (direction === "prev" && offset > 0) {
			onOffsetChange(Math.max(0, offset - limit));
			setPendingEdgeNav("last");
		}
	};

	const handleEditTools = async () => {
		setShowDetailSheet(false);
		setSelectedMCPClient(null);
		if (refetch) {
			await refetch();
		}
	};

	const hasActiveFilters = Boolean(debouncedSearch) || Boolean(server) || filtersActive;

	// True empty state: no servers at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				{formOpen && <ClientForm open={formOpen} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
				<MCPServersEmptyState onAddClick={handleCreate} canCreate={hasCreateMCPClientAccess} />
			</>
		);
	}

	return (
		<div className="flex grow flex-col overflow-auto">
			{showDetailSheet && selectedMCPClient && (
				<MCPClientSheet
					mcpClient={selectedMCPClient}
					onClose={handleDetailSheetClose}
					onSubmitSuccess={handleEditTools}
					onNavigate={handleDetailNavigate}
					hasPrev={selectedMCPClientIndex > 0 || offset > 0}
					hasNext={(selectedMCPClientIndex >= 0 && selectedMCPClientIndex < mcpClients.length - 1) || offset + limit < totalCount}
				/>
			)}
			<AlertDialog open={!!clientToDelete} onOpenChange={(open) => !open && setClientToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove MCP Server</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to remove MCP server {clientToDelete?.config.name}? You will need to reconnect the server to continue
							using it.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								if (clientToDelete) void handleDelete(clientToDelete);
							}}
							className="bg-destructive hover:bg-destructive/90"
						>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
			{bootstrapAuthorize && (
				<OAuth2Authorizer
					open={!!bootstrapAuthorize}
					onClose={() => setBootstrapAuthorize(null)}
					onSuccess={async () => {
						toast({
							title: "Success",
							description: bootstrapAuthorize.isPerUserOauth
								? "OAuth setup verified successfully. Each user will authenticate individually."
								: "MCP client connected successfully",
						});
						setBootstrapAuthorize(null);
						if (refetch) {
							await refetch();
						}
					}}
					onError={(error) => {
						toast({ title: "Authorization failed", description: error, variant: "destructive" });
					}}
					onConflict={(error) => {
						setBootstrapAuthorize(null);
						toast({ title: "Authorization failed", description: error, variant: "destructive" });
					}}
					authorizeUrl={bootstrapAuthorize.authorizeUrl}
					oauthConfigId={bootstrapAuthorize.oauthConfigId}
					mcpClientId={bootstrapAuthorize.mcpClientId}
					isPerUserOauth={bootstrapAuthorize.isPerUserOauth}
				/>
			)}
			{bootstrapHeadersClient && (
				<MCPHeadersAuthorizer
					open={!!bootstrapHeadersClient}
					onClose={() => setBootstrapHeadersClient(null)}
					onSuccess={async () => {
						toast({
							title: "Success",
							description: "Headers verified successfully. Each user will submit their own values when using this MCP server.",
						});
						setBootstrapHeadersClient(null);
						if (refetch) {
							await refetch();
						}
					}}
					onError={() => {
						/* error state rendered by the dialog itself */
					}}
					onConflict={async (error) => {
						// 409: tools were already discovered (e.g. double submit or a
						// concurrent verification) — the client is verified; refresh.
						toast({ title: "Already verified", description: error });
						setBootstrapHeadersClient(null);
						if (refetch) {
							await refetch();
						}
					}}
					perUserHeaderKeys={bootstrapHeadersClient.config.per_user_header_keys ?? []}
					submitHandler={async (values) => {
						await verifyMCPClientHeaders({
							id: bootstrapHeadersClient.config.client_id,
							userHeaders: values,
						}).unwrap();
					}}
				/>
			)}

			{/* Mirrors OAuth2Authorizer's confirm-step layout (icon header, muted
			    info box, outline-cancel + default-continue footer) so token_exchange
			    reverification reads as the same kind of admin-credential action,
			    not a destructive one. */}
			<Dialog
				open={!!exchangeVerifyClient}
				onOpenChange={(next) => {
					// Keep the dialog open (with a spinner) for the duration of the
					// verify call itself, so there's visible feedback while the
					// backend does the token exchange + live connect + tools/list
					// round trip, instead of closing immediately on "Continue" and
					// leaving nothing on screen until the toast lands.
					if (!next && !(exchangeVerifyClient && verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id))) {
						setExchangeVerifyClient(null);
					}
				}}
			>
				<DialogContent className="gap-0 overflow-hidden p-0 sm:max-w-md">
					<DialogHeader className="border-b px-5 py-4 text-left">
						<div className="flex items-start gap-3">
							<IconWrap
								variant={
									exchangeVerifyClient && verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id) ? "info" : "muted"
								}
								icon={
									exchangeVerifyClient && verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id) ? (
										<Loader2 className="size-4 animate-spin" />
									) : (
										<KeyRound className="size-4" />
									)
								}
							/>
							<div className="min-w-0 space-y-0.5">
								<DialogTitle className="text-sm leading-snug font-medium">
									{exchangeVerifyClient?.state === "pending_verification" ? "Verify as me" : "Re-verify as me"}
								</DialogTitle>
								<DialogDescription className="text-xs leading-relaxed">
									{exchangeVerifyClient?.state === "pending_verification"
										? "Establish Bifrost's discovery credential using your identity."
										: "Renew Bifrost's own discovery credential using your identity."}
								</DialogDescription>
							</div>
						</div>
					</DialogHeader>
					<div className="space-y-3 px-5 py-4">
						<InfoBox icon={<KeyRound className="size-4" />}>
							<p>
								This exchanges your own signed-in identity to{" "}
								{exchangeVerifyClient?.state === "pending_verification" ? "establish" : "renew"} Bifrost&apos;s discovery credential for{" "}
								<strong>{exchangeVerifyClient?.config.name}</strong>.
							</p>
							{exchangeVerifyClient?.state === "pending_verification" ? (
								<p className="text-muted-foreground/80 text-xs">
									That credential is only used to periodically fetch this server&apos;s tool list, not for real user requests, whose
									tokens are exchanged automatically on every request.
								</p>
							) : (
								<p className="text-muted-foreground/80 text-xs">
									That credential is only used to periodically fetch this server&apos;s tool list, not for real user requests, whose tokens
									are exchanged automatically on every request. You only need this if the credential badge shows it&apos;s expired, but
									running it any time is safe.
								</p>
							)}
						</InfoBox>
						<div className="flex justify-end gap-2">
							<Button
								size="sm"
								variant="outline"
								disabled={exchangeVerifyClient ? verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id) : false}
								onClick={() => setExchangeVerifyClient(null)}
								data-testid="verify-exchange-cancel-btn"
							>
								Cancel
							</Button>
							<Button
								size="sm"
								disabled={exchangeVerifyClient ? verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id) : false}
								onClick={async () => {
									if (!exchangeVerifyClient) return;
									const client = exchangeVerifyClient;
									await handleVerifyExchange(client);
									setExchangeVerifyClient(null);
								}}
								data-testid="verify-exchange-confirm-btn"
							>
								{exchangeVerifyClient && verifyingExchangeClients.includes(exchangeVerifyClient.config.client_id) ? (
									<Loader2 className="size-3.5 animate-spin" />
								) : null}
								Continue
							</Button>
						</div>
					</div>
				</DialogContent>
			</Dialog>

			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">MCP Server Catalog</h2>
					<p className="text-muted-foreground text-sm">Manage servers that can connect to the MCP Tools endpoint.</p>
				</div>
				<div className="flex gap-2">
					<MCPUsageGuideSheet />
					<Button asChild variant="outline" data-testid="mcp-library-link-btn" className="h-8">
						<Link to="/workspace/mcp-registry/library">
							<Box />
							<span className="hidden sm:inline">Library</span>
						</Link>
					</Button>
					<Button
						onClick={handleCreate}
						disabled={!hasCreateMCPClientAccess}
						data-testid="create-mcp-client-btn"
						aria-label="New MCP Server"
						className="h-8 gap-2"
					>
						<Plus />
						<span className="hidden sm:inline">New MCP Server</span>
					</Button>
				</div>
			</div>

			{/* Toolbar: Search */}
			<div className="mb-4 flex items-center gap-3">
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label="Search MCP servers by name"
						placeholder="Search by name..."
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="mcp-clients-search-input"
					/>
				</div>
				{server && (
					<Button
						variant="outline"
						size="sm"
						className="h-8 gap-2"
						onClick={onServerFilterClear}
						data-testid="mcp-client-server-filter-clear-btn"
					>
						Server filter
						<X className="size-3" />
					</Button>
				)}
			</div>

			<div className="flex grow flex-col overflow-hidden">
				<div className="mb-2 grow overflow-hidden rounded-sm border">
					<Table
						data-testid="mcp-clients-table"
						containerClassName="h-full overflow-auto"
						className="w-full min-w-[1516px] table-fixed"
					>
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead className="w-[260px] font-semibold">Name</TableHead>
								<TableHead className="w-[150px] font-semibold">Connection Type</TableHead>
								<TableHead className="w-[150px] font-semibold">Auth Type</TableHead>
								<TableHead className="w-[140px] font-semibold">Auth Scope</TableHead>
								<TableHead className="w-[120px] font-semibold">Code Mode</TableHead>
								<TableHead className="w-[120px] font-semibold">VK Access</TableHead>
								<TableHead className="w-[130px] font-semibold">Enabled Tools</TableHead>
								<TableHead className="w-[160px] font-semibold">Auto-execute Tools</TableHead>
								<TableHead className="w-[140px] font-semibold">
									<HeaderWithTooltip
										label="State"
										tooltip={
											<>
												<p>
													The client's connection state (healthy, unstable, needs re-authorization, and so on). "Unstable" reflects
													Bifrost's own connection checks to the server, not the results of tool calls made through it: it self-heals and
													never blocks tool calls. For per-user clients (OAuth, headers, token exchange), this reflects Bifrost's own
													retained admin credential (used only to periodically refresh the tool list), not any individual user's own
													session, which is unaffected either way.
												</p>
												<a
													data-testid="mcp-client-state-link"
													href="https://docs.getbifrost.ai/mcp/connections"
													target="_blank"
													rel="noreferrer"
													className="text-primary mt-2 inline-block underline"
												>
													See all connection states
												</a>
											</>
										}
									/>
								</TableHead>
								<TableHead className="w-[90px] font-semibold">Status</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-10 w-14 text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{mcpClients.length === 0 ? (
								<TableRow>
									<TableCell colSpan={11} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching MCP servers found.</span>
									</TableCell>
								</TableRow>
							) : (
								mcpClients.map((c: MCPClient) => {
									const canReconnect = canReconnectMCPClient(c.config);
									const enabledToolsCount =
										c.state == "healthy"
											? c.config.tools_to_execute?.includes("*")
												? c.tools?.length
												: (c.config.tools_to_execute?.length ?? 0)
											: 0;
									const autoExecuteToolsCount =
										c.state == "healthy"
											? c.config.tools_to_auto_execute?.includes("*")
												? c.tools?.length
												: (c.config.tools_to_auto_execute?.length ?? 0)
											: 0;
									return (
										<TableRow key={c.config.client_id} className="group hover:bg-muted/50 transition-colors">
											<TableCell className="font-medium">
												<div className="truncate" title={c.config.name}>
													{c.config.name}
												</div>
											</TableCell>
											<TableCell data-testid="mcp-client-connection-type">
												<Badge variant="outline" className="font-mono">
													{getConnectionTypeDisplay(c.config.connection_type)}
												</Badge>
											</TableCell>
											<TableCell data-testid="mcp-client-auth-type">{getAuthTypeDisplay(c.config.auth_type)}</TableCell>
											<TableCell data-testid="mcp-client-auth-scope">{getAuthScopeDisplay(c.config.auth_type)}</TableCell>
											<TableCell>
												<Badge
													className={
														c.state == "healthy"
															? c.config.is_code_mode_client
																? "bg-green-100 text-green-800"
																: "bg-gray-100 text-gray-800"
															: ""
													}
												>
													{c.state == "healthy" ? <>{c.config.is_code_mode_client ? "Enabled" : "Disabled"}</> : "-"}
												</Badge>
											</TableCell>
											<TableCell data-testid="mcp-client-vk-access">
												{c.config.allow_on_all_virtual_keys
													? "All"
													: c.vk_configs?.length
														? `${c.vk_configs.length} ${c.vk_configs.length === 1 ? "VK" : "VKs"}`
														: "None"}
											</TableCell>
											<TableCell>
												{c.state == "healthy" ? (
													<>
														{enabledToolsCount}/{c.tools?.length}
													</>
												) : (
													"-"
												)}
											</TableCell>
											<TableCell>
												{c.state == "healthy" ? (
													<>
														{autoExecuteToolsCount}/{c.tools?.length}
													</>
												) : (
													"-"
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												{/* Every state (healthy/unstable/needs_reauth/pending_verification)
												    now carries a real, meaningful signal for per-user clients too,
												    the connection checker's periodic admin-discovery check for
												    per-user auth types, so this is just the badge uniformly,
												    same as shared clients. Column-header tooltip above explains
												    that "unstable" reflects Bifrost's own connection checks, not
												    caller traffic. "degraded" additionally gets a drill-down —
												    see StateBadge below. */}
												<StateBadge state={c.state} nodeStates={c.node_states} />
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<Switch
													data-testid={`mcp-client-enabled-switch-${c.config.client_id}`}
													checked={!c.config.disabled}
													size="md"
													disabled={!hasUpdateMCPClientAccess || togglingClientIds.has(c.config.client_id)}
													onAsyncCheckedChange={async (checked) => {
														setTogglingClientIds((prev) => new Set(prev).add(c.config.client_id));
														// PUT has PATCH semantics: omitted fields keep their stored value.
														// Send only `disabled` — echoing back fields from the GET response
														// re-submits them in the response's units, which differ from the
														// units PUT expects (e.g. tool_sync_interval is ns out, minutes in).
														await updateMCPClient({
															id: c.config.client_id,
															data: {
																disabled: !checked,
															},
														})
															.unwrap()
															.then(() => {
																toast({ title: `Server ${checked ? "enabled" : "disabled"} successfully` });
																if (refetch) refetch();
															})
															.catch((err) => {
																toast({ title: "Error", description: getErrorMessage(err), variant: "destructive" });
															})
															.finally(() => {
																setTogglingClientIds((prev) => {
																	const next = new Set(prev);
																	next.delete(c.config.client_id);
																	return next;
																});
															});
													}}
												/>
											</TableCell>
											<TableCell
												className={`bg-card group-hover:bg-muted/50 sticky right-0 z-10 text-right ${PIN_SHADOW_RIGHT}`}
												onClick={(e) => e.stopPropagation()}
											>
												<MCPClientActionsMenu
													client={c}
													hasUpdateAccess={hasUpdateMCPClientAccess}
													hasDeleteAccess={hasDeleteMCPClientAccess}
													isReconnecting={reconnectingClients.includes(c.config.client_id)}
													isAuthorizing={authorizingClients.includes(c.config.client_id)}
													isReauthorizing={reauthorizingClients.includes(c.config.client_id)}
													isVerifyingExchange={verifyingExchangeClients.includes(c.config.client_id)}
													canReconnect={canReconnect}
													onEdit={handleRowClick}
													onReconnect={(client) => void handleReconnect(client)}
													onAuthorize={(client) => void handleStartBootstrap(client)}
													onReauthorize={(client) => void handleReauthorize(client)}
													onRefreshHeaders={handleRefreshHeaders}
													onVerifyExchange={handleRequestVerifyExchange}
													onDelete={setClientToDelete}
												/>
											</TableCell>
										</TableRow>
									);
								})
							)}
						</TableBody>
					</Table>
				</div>

				{/* Pagination */}
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
								data-testid="mcp-clients-pagination-prev-btn"
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
								data-testid="mcp-clients-pagination-next-btn"
								aria-label="Next page"
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>

			{formOpen && <ClientForm open={formOpen} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
			{reauthorizeFlow && (
				<OAuth2Authorizer
					open={!!reauthorizeFlow}
					onClose={() => setReauthorizeFlow(null)}
					onSuccess={() => {
						toast({
							title: "Success",
							description: reauthorizeFlow.isPerUserOauth
								? "Admin discovery credential refreshed successfully."
								: "MCP client re-authorized successfully",
						});
						setReauthorizeFlow(null);
						if (refetch) void refetch();
					}}
					onError={(error) => {
						toast({ title: "Reauthorization failed", description: error, variant: "destructive" });
					}}
					onConflict={() => {
						// 409: the flow's completion raced (popup postMessage vs.
						// status polling both call complete-oauth) or this was a
						// double submit. Either way the credential is already live
						// server-side, so treat it as success rather than an error.
						toast({
							title: "Success",
							description: reauthorizeFlow.isPerUserOauth
								? "Admin discovery credential refreshed successfully."
								: "MCP client re-authorized successfully",
						});
						setReauthorizeFlow(null);
						if (refetch) void refetch();
					}}
					authorizeUrl={reauthorizeFlow.authorizeUrl}
					oauthConfigId={reauthorizeFlow.oauthConfigId}
					mcpClientId={reauthorizeFlow.mcpClientId}
					isPerUserOauth={reauthorizeFlow.isPerUserOauth}
					isReauthorize
				/>
			)}
			{headersRefreshFlow && (
				<MCPHeadersAuthorizer
					open={!!headersRefreshFlow}
					onClose={() => setHeadersRefreshFlow(null)}
					onSuccess={() => {
						toast({ title: "Success", description: "Admin discovery credential refreshed successfully." });
						setHeadersRefreshFlow(null);
						if (refetch) void refetch();
					}}
					onError={() => {
						/* error state rendered by the dialog itself */
					}}
					onConflict={(error) => {
						// 409: the flow's completion raced (double submit / concurrent
						// verification) or the credential no longer needed a refresh;
						// either way the client is fine, so treat it as success.
						toast({ title: "Already verified", description: error });
						setHeadersRefreshFlow(null);
						if (refetch) void refetch();
					}}
					perUserHeaderKeys={headersRefreshFlow.perUserHeaderKeys}
					submitHandler={async (values) => {
						await verifyMCPClientHeaders({
							id: headersRefreshFlow.mcpClientId,
							userHeaders: values,
						}).unwrap();
					}}
				/>
			)}
		</div>
	);
}

// StateBadge renders the plain state badge, except for "degraded" — a
// distributed deployment's instances currently disagreeing about a client's
// state — which additionally gets a hover drill-down showing the
// per-instance breakdown behind the aggregate. summarizeNodeStates groups
// instance IDs by their reported state so the drill-down reads as counts
// ("2 instances: Healthy, 1 instance: Unstable") rather than a raw ID list.
function summarizeNodeStates(nodeStates: Record<string, string>): string[] {
	const countByState = new Map<string, number>();
	for (const state of Object.values(nodeStates)) {
		countByState.set(state, (countByState.get(state) ?? 0) + 1);
	}
	return Array.from(countByState.entries()).map(
		([state, count]) => `${count} ${count === 1 ? "instance" : "instances"}: ${titleCaseFromSnakeCase(state)}`,
	);
}

function StateBadge({ state, nodeStates }: { state: string; nodeStates?: Record<string, string> }) {
	const badge = <Badge className={MCP_STATUS_COLORS[state]}>{titleCaseFromSnakeCase(state)}</Badge>;
	if (state !== "degraded" || !nodeStates || Object.keys(nodeStates).length === 0) {
		return badge;
	}
	return (
		<Popover>
			<PopoverTrigger asChild>
				<button type="button" data-testid="mcp-client-state-degraded-trigger" className="cursor-help">
					{badge}
				</button>
			</PopoverTrigger>
			<PopoverContent className="w-xs text-xs" align="start">
				<p className="text-muted-foreground mb-1.5">Instances disagree about this client&apos;s state:</p>
				<ul className="space-y-0.5">
					{summarizeNodeStates(nodeStates).map((line) => (
						<li key={line}>{line}</li>
					))}
				</ul>
			</PopoverContent>
		</Popover>
	);
}

function HeaderWithTooltip({ label, tooltip }: { label: string; tooltip: ReactNode }) {
	return (
		<Popover>
			<PopoverTrigger asChild>
				<button
					type="button"
					aria-label={`${label} column guidance`}
					data-testid="mcp-client-state-info-trigger"
					className="inline-flex cursor-help items-center gap-2"
				>
					{label}
					<Info className="text-muted-foreground size-3" />
				</button>
			</PopoverTrigger>
			<PopoverContent className="w-xs text-xs" align="start">
				{tooltip}
			</PopoverContent>
		</Popover>
	);
}