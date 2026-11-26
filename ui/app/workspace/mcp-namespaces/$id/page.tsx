// SBS fork: MetaMCP-style namespace detail page.
import VirtualMCPAccessTab from "@/app/workspace/virtual-mcps/views/virtualMcpAccessTab";
import {
	ConnectionStateBadge,
	ConnectionTypeBadge,
	CopyField,
	InfoCard,
	SectionHeader,
	StatusBadge,
	formatDate,
} from "@/components/sbs/mcpShared";
import PageTitle from "@/components/pageTitle";
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { mcpEndpointUrl } from "@/lib/sbs/mcpClient";
import {
	followsFutureTools,
	gatewayToolName,
	isServerActive,
	isToolActive,
	overrideIsEmpty,
	removeServer,
	setServerActive,
	setToolActive,
	specFor,
	stripOverrides,
	TOOL_WILDCARD,
} from "@/lib/sbs/namespace";
import {
	getErrorMessage,
	useAttachVirtualMCPVirtualKeyMutation,
	useDetachVirtualMCPVirtualKeyMutation,
	useRefreshMCPClientToolsMutation,
	useDeleteVirtualMCPMutation,
	useGetMCPClientsQuery,
	useGetSBSEndpointsQuery,
	useGetVirtualMCPQuery,
	usePutSBSToolOverridesMutation,
	useUpdateVirtualMCPMutation,
} from "@/lib/store";
import type { MCPClient, MCPClientTool } from "@/lib/types/mcp";
import type { SBSOverridesMap, SBSToolOverride } from "@/lib/types/sbsMcp";
import type { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link, useNavigate } from "@tanstack/react-router";
import { KeyRound, Loader2, Pencil, Plus, RefreshCw, Search, SearchCode, Trash2, Undo2, X } from "lucide-react";
import { Fragment, useEffect, useMemo, useState } from "react";

interface ToolRow {
	client: MCPClient;
	tool: MCPClientTool;
	gatewayName: string;
	override?: SBSToolOverride;
	serverActive: boolean;
	active: boolean;
}

export default function NamespaceDetailPage({ id }: { id: number }) {
	const navigate = useNavigate();
	const { toast } = useToast();
	const canUpdate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.VirtualMCPs, RbacOperation.Delete);

	const { data: ns, isLoading, isError, error } = useGetVirtualMCPQuery(id);
	const { data: clientsData } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const { data: endpoints } = useGetSBSEndpointsQuery();
	const [updateVirtualMCP, { isLoading: saving }] = useUpdateVirtualMCPMutation();
	const [putOverrides] = usePutSBSToolOverridesMutation();
	const [deleteVirtualMCP] = useDeleteVirtualMCPMutation();
	const [attachVk] = useAttachVirtualMCPVirtualKeyMutation();
	const [detachVk] = useDetachVirtualMCPVirtualKeyMutation();
	const [refreshClientTools] = useRefreshMCPClientToolsMutation();
	const [refreshing, setRefreshing] = useState(false);
	const [tab, setTab] = useState("servers");
	// Virtual keys allowed to use the namespace, staged until "Save access".
	const [assignedVkIds, setAssignedVkIds] = useState<string[]>([]);
	const [savingAccess, setSavingAccess] = useState(false);
	const savedVkKey = JSON.stringify([...(ns?.virtual_key_ids ?? [])].sort());
	useEffect(() => {
		setAssignedVkIds(JSON.parse(savedVkKey) as string[]);
	}, [savedVkKey]);
	const accessDirty = JSON.stringify([...assignedVkIds].sort()) !== savedVkKey;

	const [editing, setEditing] = useState(false);
	const [confirmDelete, setConfirmDelete] = useState(false);
	const [addServerId, setAddServerId] = useState<string>("");
	const [toolSearch, setToolSearch] = useState("");
	const [toolServer, setToolServer] = useState<string>("all");
	const [toolStatus, setToolStatus] = useState<"all" | "active" | "inactive">("all");
	const [openEditor, setOpenEditor] = useState<string | null>(null);

	const clients = clientsData?.clients ?? [];
	const clientById = useMemo(() => new Map(clients.map((c) => [c.config.client_id, c])), [clients]);
	const specs: VirtualMCPToolSpec[] = ns?.tools ?? [];
	const nsEndpoints = (endpoints ?? []).filter((e) => e.virtual_mcp_id === id);

	const toolRows: ToolRow[] = useMemo(() => {
		const rows: ToolRow[] = [];
		for (const spec of specs) {
			const client = clientById.get(spec.mcp_client_id);
			if (!client) continue;
			for (const tool of client.tools ?? []) {
				rows.push({
					client,
					tool,
					gatewayName: gatewayToolName(client.config.name, tool.name),
					override: spec.overrides?.[tool.name],
					serverActive: isServerActive(spec),
					active: isServerActive(spec) && isToolActive(spec, tool.name),
				});
			}
		}
		return rows.sort((a, b) => a.gatewayName.localeCompare(b.gatewayName));
	}, [specs, clientById]);

	if (isLoading) {
		return (
			<div className="flex grow items-center justify-center">
				<Loader2 className="h-6 w-6 animate-spin" />
			</div>
		);
	}
	if (isError || !ns) {
		return (
			<div className="p-6">
				<PageTitle breadcrumbs={[{ label: "Namespaces", to: "/workspace/mcp-namespaces" }, { label: "Not found" }]} />
				<p className="text-destructive text-sm">Could not load the namespace: {getErrorMessage(error)}</p>
			</div>
		);
	}

	const saveSpecs = async (next: VirtualMCPToolSpec[], message: string) => {
		try {
			await updateVirtualMCP({ id, data: { tools: stripOverrides(next) } }).unwrap();
			toast({ title: message });
		} catch (err) {
			toast({ title: "Update failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const overridesMap = (): SBSOverridesMap => {
		const map: SBSOverridesMap = {};
		for (const spec of specs) map[spec.mcp_client_id] = { ...(spec.overrides ?? {}) };
		return map;
	};

	const saveOverride = async (clientId: string, tool: string, override: SBSToolOverride | null) => {
		const map = overridesMap();
		map[clientId] = map[clientId] ?? {};
		if (override && !overrideIsEmpty(override)) map[clientId][tool] = override;
		else delete map[clientId][tool];
		try {
			await putOverrides({ id, overrides: map }).unwrap();
			toast({ title: override ? "Override saved" : "Override removed" });
			setOpenEditor(null);
		} catch (err) {
			toast({ title: "Saving the override failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const refreshAll = async () => {
		setRefreshing(true);
		const results = await Promise.allSettled(specs.map((s) => refreshClientTools(s.mcp_client_id).unwrap()));
		setRefreshing(false);
		const failed = results.filter((r) => r.status === "rejected").length;
		const tools = results.reduce((n, r) => n + (r.status === "fulfilled" ? (r.value.tool_count ?? 0) : 0), 0);
		toast({
			title: failed ? `Refreshed ${specs.length - failed} of ${specs.length} servers` : `Refreshed ${specs.length} servers: ${tools} tools`,
			variant: failed ? "destructive" : "default",
		});
	};

	const saveAccess = async () => {
		const original = new Set<string>(JSON.parse(savedVkKey) as string[]);
		const staged = new Set(assignedVkIds);
		setSavingAccess(true);
		try {
			for (const vkId of assignedVkIds.filter((v) => !original.has(v))) await attachVk({ id, vkId }).unwrap();
			for (const vkId of [...original].filter((v) => !staged.has(v))) await detachVk({ id, vkId }).unwrap();
			toast({ title: "Access saved" });
		} catch (err) {
			toast({ title: "Saving access failed", description: getErrorMessage(err), variant: "destructive" });
		} finally {
			setSavingAccess(false);
		}
	};

	const activeServers = specs.filter((s) => isServerActive(s)).length;
	const activeTools = toolRows.filter((r) => r.active).length;
	const overridden = toolRows.filter((r) => !overrideIsEmpty(r.override)).length;
	const availableToAdd = clients.filter((c) => !specFor(specs, c.config.client_id));

	const filteredTools = toolRows.filter((r) => {
		if (toolServer !== "all" && r.client.config.client_id !== toolServer) return false;
		if (toolStatus === "active" && !r.active) return false;
		if (toolStatus === "inactive" && r.active) return false;
		const q = toolSearch.trim().toLowerCase();
		if (!q) return true;
		return [r.gatewayName, r.override?.name, r.tool.description, r.override?.description, r.tool.title].some((v) => (v ?? "").toLowerCase().includes(q));
	});

	return (
		<div className="flex grow flex-col gap-4 overflow-auto pb-6">
			<PageTitle
				breadcrumbs={[{ label: "Namespaces", to: "/workspace/mcp-namespaces" }, { label: ns.name }]}
				tags={[{ label: ns.enabled ? "Enabled" : "Disabled", tone: ns.enabled ? "success" : "neutral" }]}
			>
				{ns.description || "No description"}
			</PageTitle>

			<div className="flex flex-wrap items-center gap-2">
				<Button variant="outline" size="sm" onClick={() => setEditing(true)} disabled={!canUpdate}>
					<Pencil className="h-4 w-4" /> Edit
				</Button>
				<Button variant="outline" size="sm" onClick={refreshAll} disabled={refreshing || specs.length === 0 || !canUpdate}>
					<RefreshCw className={`h-4 w-4 ${refreshing ? "animate-spin" : ""}`} /> Refresh tools
				</Button>
				<Button variant="outline" size="sm" asChild>
					<Link to="/workspace/mcp-inspector" search={{ target: `vmcp:${ns.endpoint_slug}` }}>
						<SearchCode className="h-4 w-4" /> Inspect
					</Link>
				</Button>
				<div className="ml-2 flex items-center gap-2 text-sm">
					<Switch
						checked={ns.enabled}
						disabled={!canUpdate}
						onCheckedChange={async (enabled) => {
							try {
								await updateVirtualMCP({ id, data: { enabled } }).unwrap();
							} catch (err) {
								toast({ title: "Update failed", description: getErrorMessage(err), variant: "destructive" });
							}
						}}
					/>
					Enabled
				</div>
				{canDelete && (
					<Button variant="outline" size="sm" className="text-destructive ml-auto" onClick={() => setConfirmDelete(true)}>
						<Trash2 className="h-4 w-4" /> Delete namespace
					</Button>
				)}
			</div>

			<div className="grid gap-4 md:grid-cols-2">
				<InfoCard
					title="Basic information"
					rows={[
						{ label: "ID", value: <span className="font-mono">{ns.id}</span> },
						{ label: "Slug", value: <span className="font-mono">{ns.endpoint_slug}</span> },
						{ label: "Created", value: formatDate(ns.created_at) },
						{ label: "Last updated", value: formatDate(ns.updated_at) },
						{ label: "Virtual keys", value: `${ns.virtual_key_ids?.length ?? 0} assigned` },
					]}
				/>
				<InfoCard
					title="Statistics"
					rows={[
						{ label: "MCP servers", value: `${activeServers} active / ${specs.length}` },
						{ label: "Tools", value: `${activeTools} active / ${toolRows.length}` },
						{ label: "Overridden tools", value: overridden },
						{ label: "Endpoints", value: nsEndpoints.length },
						{
							label: "Server types",
							value: (
								<span className="flex flex-wrap justify-end gap-1">
									{Array.from(new Set(specs.map((s) => clientById.get(s.mcp_client_id)?.config.connection_type).filter(Boolean))).map((t) => (
										<ConnectionTypeBadge key={t} type={t} />
									))}
								</span>
							),
						},
					]}
				/>
			</div>

			<Tabs value={tab} onValueChange={setTab} className="w-full">
				<TabsList>
					<TabsTrigger value="servers">MCP servers ({specs.length})</TabsTrigger>
					<TabsTrigger value="tools">Tools ({activeTools}/{toolRows.length})</TabsTrigger>
					<TabsTrigger value="endpoints">Endpoints ({nsEndpoints.length})</TabsTrigger>
					<TabsTrigger value="access">
						Access ({ns.virtual_key_ids?.length ?? 0}){accessDirty && " •"}
					</TabsTrigger>
					<TabsTrigger value="prompts">Prompts &amp; resources</TabsTrigger>
				</TabsList>

				{/* ---------------- Servers ---------------- */}
				<TabsContent value="servers" className="mt-4">
					<SectionHeader title="MCP servers" description="Inactive servers stay in the namespace (with their overrides) but serve nothing.">
						{canUpdate && availableToAdd.length > 0 && (
							<>
								<Select value={addServerId} onValueChange={setAddServerId}>
									<SelectTrigger className="h-8 w-56">
										<SelectValue placeholder="Add an MCP server" />
									</SelectTrigger>
									<SelectContent>
										{availableToAdd.map((c) => (
											<SelectItem key={c.config.client_id} value={c.config.client_id}>
												{c.config.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<Button
									size="sm"
									disabled={!addServerId || saving}
									onClick={async () => {
										await saveSpecs([...specs, { mcp_client_id: addServerId, tool_names: [TOOL_WILDCARD] }], "Server added");
										setAddServerId("");
									}}
								>
									<Plus className="h-4 w-4" /> Add
								</Button>
							</>
						)}
					</SectionHeader>
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead>Server</TableHead>
									<TableHead>Type</TableHead>
									<TableHead>Connection</TableHead>
									<TableHead>Active tools</TableHead>
									<TableHead>Status</TableHead>
									<TableHead className="w-10" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{specs.length === 0 && (
									<TableRow>
										<TableCell colSpan={6} className="text-muted-foreground py-8 text-center text-sm">
											No servers yet. Add one above.
										</TableCell>
									</TableRow>
								)}
								{specs.map((spec) => {
									const client = clientById.get(spec.mcp_client_id);
									const total = client?.tools?.length ?? 0;
									const active = !isServerActive(spec) ? 0 : followsFutureTools(spec) ? total : spec.tool_names.length;
									return (
										<TableRow key={spec.mcp_client_id}>
											<TableCell className="font-medium">
												{client ? (
													<Link to="/workspace/mcp-servers/$id" params={{ id: client.config.client_id }} className="hover:underline">
														{client.config.name}
													</Link>
												) : (
													<span className="text-destructive">Missing server ({spec.mcp_client_id})</span>
												)}
											</TableCell>
											<TableCell>
												<ConnectionTypeBadge type={client?.config.connection_type} />
											</TableCell>
											<TableCell>
												<ConnectionStateBadge state={client?.state} />
											</TableCell>
											<TableCell>
												{active}/{total}
												{followsFutureTools(spec) && (
													<Tooltip>
														<TooltipTrigger asChild>
															<Badge variant="outline" className="ml-2">
																+ future
															</Badge>
														</TooltipTrigger>
														<TooltipContent>Tools the server adds later are active automatically.</TooltipContent>
													</Tooltip>
												)}
											</TableCell>
											<TableCell>
												<div className="flex items-center gap-2">
													<Switch
														checked={isServerActive(spec)}
														disabled={!canUpdate || saving}
														onCheckedChange={(v) =>
															saveSpecs(setServerActive(specs, spec.mcp_client_id, v), v ? "Server activated" : "Server deactivated")
														}
													/>
													<StatusBadge active={isServerActive(spec)} />
												</div>
											</TableCell>
											<TableCell>
												{canUpdate && (
													<Button
														variant="ghost"
														size="icon"
														className="h-8 w-8"
														aria-label="Remove server from namespace"
														onClick={() => saveSpecs(removeServer(specs, spec.mcp_client_id), "Server removed")}
													>
														<X className="h-4 w-4" />
													</Button>
												)}
											</TableCell>
										</TableRow>
									);
								})}
							</TableBody>
						</Table>
					</div>
				</TabsContent>

				{/* ---------------- Tools ---------------- */}
				<TabsContent value="tools" className="mt-4">
					<SectionHeader
						title="Tools"
						description="Switch tools on or off for this namespace, and override how they are named and described to clients."
					/>
					<div className="mb-3 flex flex-wrap items-center gap-2">
						<div className="relative max-w-sm min-w-[200px] flex-1">
							<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
							<Input placeholder="Search tools" value={toolSearch} onChange={(e) => setToolSearch(e.target.value)} className="h-8 pl-9" />
						</div>
						<Select value={toolServer} onValueChange={setToolServer}>
							<SelectTrigger className="h-8 w-48">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="all">All servers</SelectItem>
								{specs.map((s) => (
									<SelectItem key={s.mcp_client_id} value={s.mcp_client_id}>
										{clientById.get(s.mcp_client_id)?.config.name ?? s.mcp_client_id}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Select value={toolStatus} onValueChange={(v) => setToolStatus(v as typeof toolStatus)}>
							<SelectTrigger className="h-8 w-36">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="all">All statuses</SelectItem>
								<SelectItem value="active">Active</SelectItem>
								<SelectItem value="inactive">Inactive</SelectItem>
							</SelectContent>
						</Select>
						<span className="text-muted-foreground ml-auto text-xs">
							{filteredTools.length} of {toolRows.length} tools
						</span>
					</div>
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead className="w-16">Active</TableHead>
									<TableHead>Tool</TableHead>
									<TableHead>Server</TableHead>
									<TableHead>Description</TableHead>
									<TableHead className="w-24" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{filteredTools.length === 0 && (
									<TableRow>
										<TableCell colSpan={5} className="text-muted-foreground py-8 text-center text-sm">
											No tools.
										</TableCell>
									</TableRow>
								)}
								{filteredTools.map((row) => {
									const key = row.gatewayName;
									const hasOverride = !overrideIsEmpty(row.override);
									const presented = row.override?.name || row.gatewayName;
									return (
										<Fragment key={key}>
											<TableRow className={row.serverActive ? "" : "opacity-50"}>
												<TableCell>
													<Switch
														checked={row.active}
														disabled={!canUpdate || saving || !row.serverActive}
														onCheckedChange={(v) =>
															saveSpecs(
																setToolActive(
																	specs,
																	row.client.config.client_id,
																	(row.client.tools ?? []).map((t) => t.name),
																	row.tool.name,
																	v,
																),
																v ? `${presented} activated` : `${presented} deactivated`,
															)
														}
													/>
												</TableCell>
												<TableCell>
													<div className="font-mono text-sm">{presented}</div>
													{row.override?.name && <div className="text-muted-foreground font-mono text-xs">{row.gatewayName}</div>}
													<div className="mt-1 flex flex-wrap gap-1">
														{hasOverride && (
															<Tooltip>
																<TooltipTrigger asChild>
																	<Badge variant="warning">Overridden</Badge>
																</TooltipTrigger>
																<TooltipContent>
																	{["name", "title", "description"]
																		.filter((f) => row.override?.[f as keyof SBSToolOverride])
																		.join(", ") || "annotations"}{" "}
																	overridden in this namespace
																</TooltipContent>
															</Tooltip>
														)}
														{row.override?.annotations && Object.keys(row.override.annotations).length > 0 && (
															<Badge variant="outline">Annotations</Badge>
														)}
														{row.tool.annotations?.readOnlyHint && <Badge variant="outline">read-only</Badge>}
														{row.tool.annotations?.destructiveHint && <Badge variant="destructive">destructive</Badge>}
													</div>
												</TableCell>
												<TableCell className="text-sm">{row.client.config.name}</TableCell>
												<TableCell className="text-muted-foreground max-w-md text-sm">
													<div className="line-clamp-2">{row.override?.description || row.tool.description || "—"}</div>
												</TableCell>
												<TableCell>
													{canUpdate && (
														<Button variant="ghost" size="sm" onClick={() => setOpenEditor(openEditor === key ? null : key)}>
															<Pencil className="h-3.5 w-3.5" /> {openEditor === key ? "Close" : "Edit"}
														</Button>
													)}
												</TableCell>
											</TableRow>
											{openEditor === key && (
												<TableRow className="bg-muted/30 hover:bg-muted/30">
													<TableCell colSpan={5}>
														<OverrideEditor row={row} onSave={(ov) => saveOverride(row.client.config.client_id, row.tool.name, ov)} />
													</TableCell>
												</TableRow>
											)}
										</Fragment>
									);
								})}
							</TableBody>
						</Table>
					</div>
				</TabsContent>

				{/* ---------------- Endpoints ---------------- */}
				<TabsContent value="endpoints" className="mt-4">
					<SectionHeader title="Endpoints" description="Endpoints serving this namespace. The namespace slug is always served directly too.">
						<Button size="sm" asChild>
							<Link to="/workspace/mcp-endpoints" search={{ create: String(ns.id) }}>
								<Plus className="h-4 w-4" /> Create endpoint
							</Link>
						</Button>
					</SectionHeader>
					<div className="space-y-3">
						<div className="rounded-md border p-3">
							<div className="mb-2 flex items-center gap-2 text-sm font-medium">
								Direct namespace URL <Badge variant="outline">virtual key required</Badge>
							</div>
							<CopyField label="MCP" value={mcpEndpointUrl(ns.endpoint_slug)} />
						</div>
						{nsEndpoints.map((ep) => (
							<div key={ep.id} className="rounded-md border p-3">
								<div className="mb-2 flex flex-wrap items-center gap-2 text-sm font-medium">
									{ep.name}
									<StatusBadge active={ep.available} activeLabel="Serving" inactiveLabel="Not serving" />
									<Badge variant="outline">{ep.auth_mode === "public" ? "public" : "virtual key required"}</Badge>
									{ep.allow_query_param_key && <Badge variant="outline">?api_key= allowed</Badge>}
								</div>
								<div className="space-y-1">
									<CopyField label="MCP" value={mcpEndpointUrl(ep.name)} />
									{ep.openapi_enabled && <CopyField label="OpenAPI" value={`${mcpEndpointUrl(ep.name)}/openapi.json`} />}
								</div>
							</div>
						))}
					</div>
				</TabsContent>

				{/* ---------------- Access ---------------- */}
				<TabsContent value="access" className="mt-4">
					<SectionHeader
						title="Access"
						description="Virtual keys allowed to use this namespace, through its endpoints and its direct URL. Public endpoints add their own key automatically."
					>
						{accessDirty && (
							<>
								<Button variant="outline" size="sm" onClick={() => setAssignedVkIds(JSON.parse(savedVkKey) as string[])} disabled={savingAccess}>
									Discard
								</Button>
								<Button size="sm" onClick={saveAccess} disabled={savingAccess || !canUpdate}>
									{savingAccess ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />} Save access
								</Button>
							</>
						)}
					</SectionHeader>
					{nsEndpoints.some((e) => e.auth_mode === "public") && (
						<p className="text-muted-foreground mb-3 text-xs">
							Public endpoint keys:{" "}
							{nsEndpoints
								.filter((e) => e.auth_mode === "public")
								.map((e) => `${e.name}${e.public_grant_added ? " (assigned by the endpoint)" : ""}`)
								.join(", ")}
							. Removing one here breaks that endpoint until it is saved again.
						</p>
					)}
					<VirtualMCPAccessTab vmcpId={id} value={assignedVkIds} onChange={setAssignedVkIds} isCreate={false} active={tab === "access"} />
				</TabsContent>

				{/* ---------------- Prompts & resources ---------------- */}
				<TabsContent value="prompts" className="mt-4">
					<SectionHeader
						title="Prompts & resources"
						description="Served from every active server in the namespace. Which ones each server exposes is set on the server's page."
					/>
					<div className="grid gap-4 md:grid-cols-2">
						{specs
							.filter((s) => isServerActive(s))
							.map((spec) => {
								const client = clientById.get(spec.mcp_client_id);
								if (!client) return null;
								return (
									<div key={spec.mcp_client_id} className="rounded-md border p-3 text-sm">
										<div className="mb-2 font-medium">
											<Link to="/workspace/mcp-servers/$id" params={{ id: client.config.client_id }} className="hover:underline">
												{client.config.name}
											</Link>
										</div>
										<div className="text-muted-foreground mb-1 text-xs uppercase">Prompts</div>
										<div className="mb-3 flex flex-wrap gap-1">
											{(client.prompts ?? []).length === 0 && <span className="text-muted-foreground">none</span>}
											{(client.prompts ?? []).map((p) => (
												<Badge key={p.name} variant="outline" className="font-mono">
													{gatewayToolName(client.config.name, p.name)}
												</Badge>
											))}
										</div>
										<div className="text-muted-foreground mb-1 text-xs uppercase">Resources</div>
										<div className="flex flex-wrap gap-1">
											{(client.resources ?? []).length + (client.resource_templates ?? []).length === 0 && (
												<span className="text-muted-foreground">none</span>
											)}
											{(client.resources ?? []).map((r) => (
												<Badge key={r.uri} variant="outline" className="font-mono">
													{r.uri}
												</Badge>
											))}
											{(client.resource_templates ?? []).map((r) => (
												<Badge key={r.uriTemplate} variant="outline" className="font-mono">
													{r.uriTemplate}
												</Badge>
											))}
										</div>
									</div>
								);
							})}
					</div>
				</TabsContent>
			</Tabs>

			{editing && <EditNamespaceDialog id={id} name={ns.name} description={ns.description ?? ""} onClose={() => setEditing(false)} />}

			<AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete namespace {ns.name}?</AlertDialogTitle>
						<AlertDialogDescription>
							It stops being served at /mcp/{ns.endpoint_slug} and through its {nsEndpoints.length} endpoint(s). The MCP servers are kept.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={async () => {
								try {
									await deleteVirtualMCP(id).unwrap();
									toast({ title: `Namespace ${ns.name} deleted` });
									navigate({ to: "/workspace/mcp-namespaces" });
								} catch (err) {
									toast({ title: "Delete failed", description: getErrorMessage(err), variant: "destructive" });
								}
							}}
						>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}

function OverrideEditor({ row, onSave }: { row: ToolRow; onSave: (ov: SBSToolOverride | null) => void }) {
	const [name, setName] = useState(row.override?.name ?? "");
	const [title, setTitle] = useState(row.override?.title ?? "");
	const [description, setDescription] = useState(row.override?.description ?? "");
	const [annotations, setAnnotations] = useState(row.override?.annotations ? JSON.stringify(row.override.annotations, null, 2) : "");
	const [jsonError, setJsonError] = useState<string | null>(null);

	const submit = () => {
		let parsed: Record<string, unknown> | undefined;
		if (annotations.trim()) {
			try {
				const value = JSON.parse(annotations);
				if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("must be a JSON object");
				// Accept both {"readOnlyHint": true} and MetaMCP's {"annotations": {...}} shape.
				parsed = (value.annotations && typeof value.annotations === "object" ? value.annotations : value) as Record<string, unknown>;
			} catch (e) {
				setJsonError(`Annotations: ${(e as Error).message}`);
				return;
			}
		}
		setJsonError(null);
		onSave({
			name: name.trim() || undefined,
			title: title.trim() || undefined,
			description: description.trim() || undefined,
			annotations: parsed,
		});
	};

	return (
		<div className="grid gap-3 py-2 md:grid-cols-2">
			<div className="space-y-1.5">
				<Label>Name</Label>
				<Input value={name} onChange={(e) => setName(e.target.value)} placeholder={row.gatewayName} className="font-mono" />
				<p className="text-muted-foreground text-xs">What clients call the tool. Letters, digits, _ . - (max 64).</p>
			</div>
			<div className="space-y-1.5">
				<Label>Title</Label>
				<Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={row.tool.title || row.tool.annotations?.title || ""} />
			</div>
			<div className="space-y-1.5 md:col-span-2">
				<Label>Description</Label>
				<Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={3} placeholder={row.tool.description ?? ""} />
			</div>
			<div className="space-y-1.5 md:col-span-2">
				<Label>Annotations (JSON, merged over the server&apos;s)</Label>
				<Textarea
					value={annotations}
					onChange={(e) => setAnnotations(e.target.value)}
					rows={3}
					className="font-mono text-xs"
					placeholder={'{ "readOnlyHint": true, "destructiveHint": false }'}
				/>
				{jsonError && <p className="text-destructive text-xs">{jsonError}</p>}
			</div>
			<div className="flex gap-2 md:col-span-2">
				<Button size="sm" onClick={submit}>
					Save override
				</Button>
				{!overrideIsEmpty(row.override) && (
					<Button size="sm" variant="outline" onClick={() => onSave(null)}>
						<Undo2 className="h-3.5 w-3.5" /> Reset to server&apos;s
					</Button>
				)}
			</div>
		</div>
	);
}

function EditNamespaceDialog({ id, name, description, onClose }: { id: number; name: string; description: string; onClose: () => void }) {
	const { toast } = useToast();
	const [nextName, setNextName] = useState(name);
	const [nextDescription, setNextDescription] = useState(description);
	const [updateVirtualMCP, { isLoading }] = useUpdateVirtualMCPMutation();
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-w-lg">
				<DialogHeader>
					<DialogTitle>Edit namespace</DialogTitle>
					<DialogDescription>The slug stays the same so existing clients keep working.</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-1.5">
						<Label>Name</Label>
						<Input value={nextName} onChange={(e) => setNextName(e.target.value)} />
					</div>
					<div className="space-y-1.5">
						<Label>Description</Label>
						<Textarea value={nextDescription} onChange={(e) => setNextDescription(e.target.value)} rows={3} />
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button
						disabled={!nextName.trim() || isLoading}
						onClick={async () => {
							try {
								await updateVirtualMCP({ id, data: { name: nextName.trim(), description: nextDescription } }).unwrap();
								toast({ title: "Namespace updated" });
								onClose();
							} catch (err) {
								toast({ title: "Update failed", description: getErrorMessage(err), variant: "destructive" });
							}
						}}
					>
						Save
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
