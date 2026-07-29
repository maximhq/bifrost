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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import {
	getErrorMessage,
	useDeleteMCPClientMutation,
	useInitiateMCPClientVerificationMutation,
	useReconnectMCPClientMutation,
	useUpdateMCPClientMutation,
	useVerifyMCPClientHeadersMutation,
} from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { Box, ChevronLeft, ChevronRight, KeyRound, Loader2, MoreHorizontal, PencilIcon, Plus, RefreshCcw, Search, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import MCPClientSheet from "./mcpClientSheet";
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
	isPerUserAuth,
	onEdit,
	onReconnect,
	onAuthorize,
	onDelete,
}: {
	client: MCPClient;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	isReconnecting: boolean;
	isAuthorizing: boolean;
	isPerUserAuth: boolean;
	onEdit: (client: MCPClient) => void;
	onReconnect: (client: MCPClient) => void;
	onAuthorize: (client: MCPClient) => void;
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
				>
					{isReconnecting || isAuthorizing ? <Loader2 className="h-4 w-4 animate-spin" /> : <MoreHorizontal className="h-4 w-4" />}
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
				{hasUpdateAccess && (
					<DropdownMenuItem
						className="cursor-pointer"
						disabled={isPerUserAuth || client.config.disabled || isReconnecting || client.state === "pending_verification" || client.state === "needs_reauth"}
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
	const [showDetailSheet, setShowDetailSheet] = useState(false);
	const { toast } = useToast();

	const [reconnectingClients, setReconnectingClients] = useState<string[]>([]);
	const [authorizingClients, setAuthorizingClients] = useState<string[]>([]);
	const [togglingClientIds, setTogglingClientIds] = useState<Set<string>>(new Set());
	// Drives the OAuth2Authorizer dialog for a config.json-bootstrapped client
	// sitting in pending_verification, triggered from the row actions menu.
	const [bootstrapAuthorize, setBootstrapAuthorize] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		popup: Window | null;
		isPerUserOauth: boolean;
	} | null>(null);
	// Drives the MCPHeadersAuthorizer dialog for a config.json-bootstrapped
	// per_user_headers client sitting in pending_verification, triggered from
	// the row actions menu.
	const [bootstrapHeadersClient, setBootstrapHeadersClient] = useState<MCPClient | null>(null);

	// RTK Query mutations
	const [reconnectMCPClient] = useReconnectMCPClientMutation();
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
		// per_user_headers takes a synchronous form-based path; OAuth-based
		// types kick off the existing browser flow below.
		if (client.config.auth_type === "per_user_headers") {
			setBootstrapHeadersClient(client);
			return;
		}
		const isPerUserOauth = client.config.auth_type === "per_user_oauth";
		// Open a blank popup synchronously, before the initiateVerification
		// await, so the click's transient user-activation is captured here
		// rather than consumed by the network round-trip — otherwise the
		// browser can block OAuth2Authorizer's later window.open entirely.
		// OAuth2Authorizer navigates this handle once authorize_url is known.
		// Not needed for per_user_oauth: that flow shows a confirm step first
		// and opens its own popup synchronously from that step's own button
		// click, so pre-opening one here would just leak an unused window.
		let popup: Window | null = null;
		if (!isPerUserOauth) {
			const width = 600;
			const height = 700;
			const left = window.screen.width / 2 - width / 2;
			const top = window.screen.height / 2 - height / 2;
			popup = window.open("", "oauth_popup", `width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`);
		}
		try {
			setAuthorizingClients((prev) => [...prev, client.config.client_id]);
			const response = await initiateVerification(client.config.client_id).unwrap();
			if (response.status === "pending_oauth" && response.authorize_url) {
				setBootstrapAuthorize({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: client.config.client_id,
					popup,
					isPerUserOauth,
				});
			} else {
				popup?.close();
				toast({
					title: "Authorization failed",
					description: "Unexpected response from server. Please try again.",
					variant: "destructive",
				});
			}
		} catch (error) {
			popup?.close();
			toast({ title: "Authorization failed", description: getErrorMessage(error), variant: "destructive" });
		} finally {
			setAuthorizingClients((prev) => prev.filter((id) => id !== client.config.client_id));
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
			default:
				return type;
		}
	};

	const getAuthScopeDisplay = (type: string | undefined) => {
		switch (type) {
			case "per_user_oauth":
			case "per_user_headers":
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
					initialPopup={bootstrapAuthorize.popup}
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

			<div className="flex grow flex-col overflow-auto">
				<div className="mb-2 grow overflow-auto rounded-sm border">
					<Table data-testid="mcp-clients-table" className="w-full min-w-[1516px] table-fixed">
						<TableHeader className="sticky top-0">
							<TableRow className="bg-muted/50">
								<TableHead className="w-[260px] font-semibold">Name</TableHead>
								<TableHead className="w-[150px] font-semibold">Connection Type</TableHead>
								<TableHead className="w-[150px] font-semibold">Auth Type</TableHead>
								<TableHead className="w-[140px] font-semibold">Auth Scope</TableHead>
								<TableHead className="w-[120px] font-semibold">Code Mode</TableHead>
								<TableHead className="w-[120px] font-semibold">VK Access</TableHead>
								<TableHead className="w-[130px] font-semibold">Enabled Tools</TableHead>
								<TableHead className="w-[160px] font-semibold">Auto-execute Tools</TableHead>
								<TableHead className="w-[140px] font-semibold">State</TableHead>
								<TableHead className="w-[90px] font-semibold">Status</TableHead>
								<TableHead className={`bg-muted/50 sticky right-0 z-10 w-14 text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
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
									// Per-user auth types (OAuth + headers) don't hold a shared
									// upstream connection, so reconnect is a no-op for them — the
									// backend's ReconnectClient rejects with ErrMCPReconnectNotApplicable.
									const isPerUserAuth = c.config.auth_type === "per_user_oauth" || c.config.auth_type === "per_user_headers";
									const enabledToolsCount =
										c.state == "connected"
											? c.config.tools_to_execute?.includes("*")
												? c.tools?.length
												: (c.config.tools_to_execute?.length ?? 0)
											: 0;
									const autoExecuteToolsCount =
										c.state == "connected"
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
														c.state == "connected" ? MCP_STATUS_COLORS[c.config.is_code_mode_client ? "connected" : "disconnected"] : ""
													}
												>
													{c.state == "connected" ? <>{c.config.is_code_mode_client ? "Enabled" : "Disabled"}</> : "-"}
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
												{c.state == "connected" ? (
													<>
														{enabledToolsCount}/{c.tools?.length}
													</>
												) : (
													"-"
												)}
											</TableCell>
											<TableCell>
												{c.state == "connected" ? (
													<>
														{autoExecuteToolsCount}/{c.tools?.length}
													</>
												) : (
													"-"
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												{isPerUserAuth ? (
													// Per-user clients never hold a shared upstream connection, so a
													// connection-state badge here would be misleading: point to the
													// per-user sessions this client actually has instead.
													<Link
														to="/workspace/mcp-sessions"
														search={{ mcp_client_id: [c.config.client_id] }}
														className="text-primary text-xs font-medium hover:underline"
														data-testid={`mcp-client-view-sessions-${c.config.client_id}`}
													>
														View sessions
													</Link>
												) : (
													<Badge className={MCP_STATUS_COLORS[c.state]}>{c.state}</Badge>
												)}
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
													isPerUserAuth={isPerUserAuth}
													onEdit={handleRowClick}
													onReconnect={(client) => void handleReconnect(client)}
													onAuthorize={(client) => void handleStartBootstrap(client)}
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
		</div>
	);
}