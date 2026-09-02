import { getExternalBaseUrl } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import PageTitle from "@/components/pageTitle";
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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useDeleteVirtualMCPMutation,
	useGetCoreConfigQuery,
	useGetMCPClientsQuery,
	useGetVirtualKeysQuery,
	useUpdateVirtualMCPMutation,
} from "@/lib/store";
import { VirtualMCP } from "@/lib/types/virtualMcps";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, Check, ChevronLeft, ChevronRight, Copy, Loader2, MoreHorizontal, Pencil, Plus, Search, Server, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";

interface VirtualMCPsTableProps {
	virtualMcps: VirtualMCP[];
	totalCount: number;
	isFetching: boolean;
	search: string;
	onSearchChange: (value: string) => void;
	hasActiveSearch: boolean;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
	onCreate: () => void;
	onEdit: (id: number) => void;
}

export default function VirtualMCPsTable({
	virtualMcps,
	totalCount,
	isFetching,
	search,
	onSearchChange,
	hasActiveSearch,
	offset,
	limit,
	onOffsetChange,
	onCreate,
	onEdit,
}: VirtualMCPsTableProps) {
	const { toast } = useToast();
	const [deleteVirtualMCP, { isLoading: deleting }] = useDeleteVirtualMCPMutation();
	const [updateVirtualMCP] = useUpdateVirtualMCPMutation();
	const canUpdate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.VirtualMCPs, RbacOperation.Delete);

	// Live clients, so "all tools" servers count their real tools and we can flag unreachable/stale ones.
	const { data: mcpClientsData, isLoading: clientsLoading, isError: clientsError } = useGetMCPClientsQuery({ limit: 1000 });
	const clientById = useMemo(() => new Map((mcpClientsData?.clients ?? []).map((c) => [c.config.client_id, c])), [mcpClientsData]);

	// Resolve assigned virtual-key ids -> names for the "Assigned to" column.
	const { data: vksData, isLoading: vksLoading } = useGetVirtualKeysQuery({ limit: 1000 });
	const vkNameById = useMemo(() => new Map((vksData?.virtual_keys ?? []).map((v) => [v.id, v.name])), [vksData]);

	// Externally reachable base URL, so the endpoint cell copies the full URL callers use.
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const baseUrl = getExternalBaseUrl(coreConfig?.client_config);

	const includedToolCount = (row: VirtualMCP) =>
		(row.tools ?? []).reduce((sum, spec) => {
			const isAll = spec.tool_names.length === 1 && spec.tool_names[0] === "*";
			return sum + (isAll ? (clientById.get(spec.mcp_client_id)?.tools?.length ?? 0) : spec.tool_names.length);
		}, 0);

	// A server is reachable only when it is present, enabled, and healthy; otherwise its tools are served
	// from the last successful sync (or not at all). Also flags specific tools no longer offered by a live
	// server (disabled at source).
	const toolWarning = (row: VirtualMCP): string | null => {
		// Until the client list loads (or if it failed), clientById is empty; don't flag every source
		// as unreachable off missing data.
		if (clientsLoading || clientsError) return null;
		let unreachable = 0;
		let unreachableServers = 0;
		let disabledAtSource = 0;
		for (const spec of row.tools ?? []) {
			const client = clientById.get(spec.mcp_client_id);
			const isAll = spec.tool_names.length === 1 && spec.tool_names[0] === "*";
			const liveNames = client?.tools?.map((t) => t.name) ?? [];
			const isReachable = !!client && !client.config.disabled && client.state === "healthy";
			if (!isReachable) {
				if (isAll) {
					// A wildcard on an unreachable server has an unknown tool count when the client is gone;
					// flag the server itself rather than count zero.
					if (liveNames.length > 0) unreachable += liveNames.length;
					else unreachableServers += 1;
				} else {
					unreachable += spec.tool_names.length;
				}
			} else if (!isAll) {
				disabledAtSource += spec.tool_names.filter((n) => !liveNames.includes(n)).length;
			}
		}
		if (unreachable === 0 && unreachableServers === 0 && disabledAtSource === 0) return null;
		const parts: string[] = [];
		if (unreachable > 0) parts.push(`${unreachable} ${unreachable === 1 ? "tool comes" : "tools come"} from an unreachable server (shown from the last successful sync)`);
		if (unreachableServers > 0) parts.push(`${unreachableServers} source ${unreachableServers === 1 ? "server is" : "servers are"} unreachable`);
		if (disabledAtSource > 0) parts.push(`${disabledAtSource} selected ${disabledAtSource === 1 ? "tool is" : "tools are"} disabled at source`);
		return parts.join(" · ");
	};
	const [pendingDelete, setPendingDelete] = useState<VirtualMCP | null>(null);
	const [pendingId, setPendingId] = useState<number | null>(null);
	const [togglingIds, setTogglingIds] = useState<Set<number>>(new Set());

	// PATCH-style update: send only `enabled`, so the toggle never touches the tools or name.
	const toggleEnabled = async (row: VirtualMCP, enabled: boolean) => {
		setTogglingIds((prev) => new Set(prev).add(row.id));
		try {
			await updateVirtualMCP({ id: row.id, data: { enabled } }).unwrap();
			toast({ title: enabled ? "Virtual MCP enabled" : "Virtual MCP disabled" });
		} catch (err) {
			toast({ title: "Failed to update Virtual MCP", description: getErrorMessage(err), variant: "destructive" });
		} finally {
			setTogglingIds((prev) => {
				const next = new Set(prev);
				next.delete(row.id);
				return next;
			});
		}
	};

	const confirmDelete = async () => {
		if (!pendingDelete) return;
		const row = pendingDelete;
		setPendingDelete(null);
		setPendingId(row.id);
		try {
			await deleteVirtualMCP(row.id).unwrap();
			// Deleting the last row on a non-zero page leaves offset past the end; step back a page.
			if (virtualMcps.length === 1 && offset > 0) {
				onOffsetChange(Math.max(0, offset - limit));
			}
			toast({ title: "Virtual MCP deleted" });
		} catch (err) {
			toast({ title: "Failed to delete Virtual MCP", description: getErrorMessage(err), variant: "destructive" });
		} finally {
			setPendingId(null);
		}
	};

	return (
		<div className="flex grow flex-col overflow-auto">
			<AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete this Virtual MCP?</AlertDialogTitle>
						<AlertDialogDescription>
							{pendingDelete?.name} will stop being served at <span className="font-mono">/mcp/{pendingDelete?.endpoint_slug}</span> and
							will be removed from any virtual keys it is assigned to. This cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="virtual-mcp-delete-cancel">Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={confirmDelete} data-testid="virtual-mcp-delete-confirm">
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<PageTitle title="Virtual MCPs">
				Bundle tools from your MCP servers into a single endpoint, then assign it to virtual keys.
			</PageTitle>

			<div className="mb-4 flex items-center justify-between gap-3">
				<div className="relative max-w-sm min-w-[200px] flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label="Search Virtual MCPs"
						placeholder="Search by name or endpoint..."
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="virtual-mcps-search-input"
					/>
				</div>
				<Button onClick={onCreate} data-testid="virtual-mcp-create-btn">
					<Plus className="h-4 w-4" />
					New Virtual MCP
				</Button>
			</div>

			<div className="flex grow flex-col overflow-auto">
				<div className={`mb-2 grow overflow-auto rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
					<Table>
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead>Name</TableHead>
								<TableHead>Endpoint</TableHead>
								<TableHead>Source servers</TableHead>
								<TableHead>Included tools</TableHead>
								<TableHead>Created</TableHead>
								<TableHead>Assigned to</TableHead>
								<TableHead>Enabled</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-10 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{virtualMcps.length === 0 ? (
								<TableRow>
									<TableCell colSpan={8} className="h-24 text-center">
										{isFetching ? (
											<div className="text-muted-foreground text-sm">Loading Virtual MCPs…</div>
										) : hasActiveSearch ? (
											<div className="text-muted-foreground text-sm">No Virtual MCPs match your search.</div>
										) : (
											<div className="flex flex-col items-center gap-3 py-6">
												<Server className="text-muted-foreground h-10 w-10" strokeWidth={1} />
												<span className="text-muted-foreground text-sm">
													No Virtual MCPs yet. Create one to bundle tools from your MCP servers into a single endpoint.
												</span>
												<Button variant="outline" size="sm" onClick={onCreate}>
													<Plus className="h-4 w-4" />
													New Virtual MCP
												</Button>
											</div>
										)}
									</TableCell>
								</TableRow>
							) : (
								virtualMcps.map((row) => {
									const warning = toolWarning(row);
									return (
									<TableRow key={row.id} className="group">
										<TableCell className="font-medium">{row.name}</TableCell>
										<TableCell>
											<EndpointCell slug={row.endpoint_slug} baseUrl={baseUrl} />
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{row.tools?.length ?? 0}</TableCell>
										<TableCell className="text-muted-foreground text-sm">
											<span className="flex items-center gap-1.5">
												{clientsLoading ? "…" : clientsError ? "—" : includedToolCount(row)}
												{warning && (
													<Tooltip>
														<TooltipTrigger asChild>
															<AlertTriangle className="size-3.5 cursor-help text-amber-500" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs">{warning}</TooltipContent>
													</Tooltip>
												)}
											</span>
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{formatDate(row.created_at)}</TableCell>
										<TableCell>
											<AssignedToCell vkIds={row.virtual_key_ids ?? []} vkNameById={vkNameById} loading={vksLoading} />
										</TableCell>
										<TableCell onClick={(e) => e.stopPropagation()}>
											<Switch
												size="md"
												checked={row.enabled}
												disabled={!canUpdate || togglingIds.has(row.id)}
												onAsyncCheckedChange={(checked) => toggleEnabled(row, checked)}
												aria-label={row.enabled ? "Disable Virtual MCP" : "Enable Virtual MCP"}
												data-testid={`virtual-mcp-enabled-switch-${row.id}`}
											/>
										</TableCell>
										<TableCell
											className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-10 bg-white text-right ${PIN_SHADOW_RIGHT}`}
											onClick={(e) => e.stopPropagation()}
										>
											<RowActions
												busy={deleting && pendingId === row.id}
												canUpdate={canUpdate}
												canDelete={canDelete}
												onEdit={() => onEdit(row.id)}
												onDelete={() => setPendingDelete(row)}
											/>
										</TableCell>
									</TableRow>
									);
								})
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
								data-testid="virtual-mcps-pagination-prev-btn"
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
								data-testid="virtual-mcps-pagination-next-btn"
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

function EndpointCell({ slug, baseUrl }: { slug: string; baseUrl: string }) {
	const fullUrl = `${baseUrl}/mcp/${slug}`;
	const { copy, copied } = useCopyToClipboard({ successMessage: "Endpoint copied" });
	return (
		<button
			type="button"
			onClick={() => copy(fullUrl)}
			className="text-muted-foreground hover:text-foreground inline-flex cursor-pointer items-center gap-1.5 font-mono text-sm transition-colors"
			aria-label="Copy endpoint URL"
			data-testid={`virtual-mcp-endpoint-copy-${slug}`}
		>
			/mcp/{slug}
			{copied ? <Check className="size-3.5 shrink-0" /> : <Copy className="size-3.5 shrink-0" />}
		</button>
	);
}

function AssignedToCell({ vkIds, vkNameById, loading }: { vkIds: string[]; vkNameById: Map<string, string>; loading: boolean }) {
	if (vkIds.length === 0) return <span className="text-muted-foreground text-sm">-</span>;
	// Hold a stable placeholder until names resolve, so badges don't reflow from ids to names.
	if (loading) return <div className="bg-muted/60 h-5 w-32 animate-pulse rounded" />;
	const VISIBLE = 2;
	const visible = vkIds.slice(0, VISIBLE);
	const overflow = vkIds.slice(VISIBLE);
	return (
		<div className="flex flex-wrap items-center gap-1">
			{visible.map((id) => (
				<Badge key={id} variant="outline" className="max-w-[180px] font-normal">
					<span className="truncate">Key: {vkNameById.get(id) ?? id}</span>
				</Badge>
			))}
			{overflow.length > 0 && (
				<Tooltip>
					<TooltipTrigger asChild>
						<Badge variant="outline" className="cursor-help font-normal">
							+{overflow.length}
						</Badge>
					</TooltipTrigger>
					<TooltipContent className="max-w-xs">{overflow.map((id) => `Key: ${vkNameById.get(id) ?? id}`).join(", ")}</TooltipContent>
				</Tooltip>
			)}
		</div>
	);
}

function RowActions({
	busy,
	canUpdate,
	canDelete,
	onEdit,
	onDelete,
}: {
	busy: boolean;
	canUpdate: boolean;
	canDelete: boolean;
	onEdit: () => void;
	onDelete: () => void;
}) {
	// A read-only user (no update or delete permission) gets no actions menu at all.
	if (!canUpdate && !canDelete) return null;
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Virtual MCP actions" disabled={busy}>
					{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <MoreHorizontal className="h-4 w-4" />}
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				{canUpdate && (
					<DropdownMenuItem
						className="cursor-pointer"
						onSelect={(e) => {
							e.preventDefault();
							onEdit();
						}}
					>
						<Pencil className="h-4 w-4" />
						Edit
					</DropdownMenuItem>
				)}
				{canDelete && (
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						onSelect={(e) => {
							e.preventDefault();
							onDelete();
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

function formatDate(iso: string): string {
	try {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t)) return iso;
		return new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
	} catch {
		return iso;
	}
}
