// SBS fork: MetaMCP-style "Endpoints": a public URL /mcp/<name> pointing at a namespace, with its own
// auth options and an OpenAPI face. Re-pointing an endpoint keeps its URL.
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { CopyButton, CopyField, StatusBadge, formatDate } from "@/components/sbs/mcpShared";
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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { mcpEndpointUrl } from "@/lib/sbs/mcpClient";
import {
	getErrorMessage,
	useCreateSBSEndpointMutation,
	useDeleteSBSEndpointMutation,
	useGetSBSEndpointsQuery,
	useGetVirtualMCPsQuery,
	useUpdateSBSEndpointMutation,
} from "@/lib/store";
import type { SBSEndpoint, SBSEndpointAuthMode, SBSEndpointRequest } from "@/lib/types/sbsMcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link, useNavigate } from "@tanstack/react-router";
import { FileJson, Loader2, MoreHorizontal, Pencil, Plus, Search, SearchCode, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";

export default function EndpointsPage({ createFor }: { createFor?: string }) {
	const navigate = useNavigate();
	const { toast } = useToast();
	const canCreate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.VirtualMCPs, RbacOperation.Delete);
	const { data: endpoints, isLoading, isFetching } = useGetSBSEndpointsQuery();
	const [updateEndpoint] = useUpdateSBSEndpointMutation();
	const [deleteEndpoint] = useDeleteSBSEndpointMutation();
	const [search, setSearch] = useState("");
	const [editing, setEditing] = useState<SBSEndpoint | "new" | null>(null);
	const [connectFor, setConnectFor] = useState<SBSEndpoint | null>(null);
	const [pendingDelete, setPendingDelete] = useState<SBSEndpoint | null>(null);

	useEffect(() => {
		if (createFor && canCreate) setEditing("new");
	}, [createFor, canCreate]);

	const rows = (endpoints ?? []).filter((e) => {
		const q = search.trim().toLowerCase();
		return (
			!q ||
			e.name.toLowerCase().includes(q) ||
			(e.virtual_mcp_name ?? "").toLowerCase().includes(q) ||
			(e.description ?? "").toLowerCase().includes(q)
		);
	});

	const toRequest = (e: SBSEndpoint): SBSEndpointRequest => ({
		name: e.name,
		description: e.description,
		virtual_mcp_id: e.virtual_mcp_id,
		enabled: e.enabled,
		auth_mode: e.auth_mode,
		public_virtual_key_id: e.public_virtual_key_id,
		allow_query_param_key: e.allow_query_param_key,
		openapi_enabled: e.openapi_enabled,
	});

	const closeEditor = () => {
		setEditing(null);
		if (createFor) navigate({ to: "/workspace/mcp-endpoints", search: {} });
	};

	return (
		<div className="flex grow flex-col overflow-auto">
			<PageTitle title="Endpoints">
				Each endpoint serves one namespace at its own URL, over MCP (Streamable HTTP) and optionally as an OpenAPI REST API. Point it at
				another namespace at any time without changing the URL.
			</PageTitle>

			<div className="mb-4 flex items-center justify-between gap-3">
				<div className="relative max-w-sm min-w-[200px] flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input placeholder="Search endpoints" value={search} onChange={(e) => setSearch(e.target.value)} className="pl-9" />
				</div>
				{canCreate && (
					<Button onClick={() => setEditing("new")}>
						<Plus className="h-4 w-4" /> Create endpoint
					</Button>
				)}
			</div>

			<div className={`grow overflow-auto rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
				<Table>
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Namespace</TableHead>
							<TableHead className="min-w-[360px]">URLs</TableHead>
							<TableHead>Auth</TableHead>
							<TableHead>Enabled</TableHead>
							<TableHead>Created</TableHead>
							<TableHead className="w-10" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading && (
							<TableRow>
								<TableCell colSpan={7} className="py-10 text-center">
									<Loader2 className="mx-auto h-5 w-5 animate-spin" />
								</TableCell>
							</TableRow>
						)}
						{!isLoading && rows.length === 0 && (
							<TableRow>
								<TableCell colSpan={7} className="text-muted-foreground py-10 text-center text-sm">
									{search ? "No endpoint matches the search." : "No endpoints yet. Create one to publish a namespace."}
								</TableCell>
							</TableRow>
						)}
						{rows.map((e) => (
							<TableRow key={e.id}>
								<TableCell className="font-medium">
									<div className="flex items-center gap-2">
										{e.name}
										{!e.available && <StatusBadge active={false} inactiveLabel="Not serving" />}
									</div>
									{e.description && <div className="text-muted-foreground max-w-[220px] truncate text-xs">{e.description}</div>}
								</TableCell>
								<TableCell>
									{e.virtual_mcp_name ? (
										<Link to="/workspace/mcp-namespaces/$id" params={{ id: String(e.virtual_mcp_id) }} className="hover:underline">
											{e.virtual_mcp_name}
										</Link>
									) : (
										<span className="text-destructive">missing namespace</span>
									)}
								</TableCell>
								<TableCell>
									<div className="space-y-1">
										<CopyField label="MCP" value={mcpEndpointUrl(e.name)} />
										{e.openapi_enabled && <CopyField label="OpenAPI" value={`${mcpEndpointUrl(e.name)}/openapi.json`} />}
									</div>
								</TableCell>
								<TableCell>
									<div className="flex flex-col items-start gap-1">
										<Badge variant={e.auth_mode === "public" ? "warning" : "outline"}>
											{e.auth_mode === "public" ? "Public" : "Virtual key"}
										</Badge>
										{e.allow_query_param_key && <Badge variant="outline">?api_key=</Badge>}
									</div>
								</TableCell>
								<TableCell>
									<Switch
										checked={e.enabled}
										disabled={!canUpdate}
										onCheckedChange={async (enabled) => {
											try {
												await updateEndpoint({ id: e.id, data: { ...toRequest(e), enabled } }).unwrap();
											} catch (err) {
												toast({ title: "Update failed", description: getErrorMessage(err), variant: "destructive" });
											}
										}}
									/>
								</TableCell>
								<TableCell className="text-muted-foreground text-xs">{formatDate(e.created_at)}</TableCell>
								<TableCell>
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Actions">
												<MoreHorizontal className="h-4 w-4" />
											</Button>
										</DropdownMenuTrigger>
										<DropdownMenuContent align="end">
											<DropdownMenuItem onClick={() => setConnectFor(e)}>
												<FileJson className="h-4 w-4" /> Connect a client
											</DropdownMenuItem>
											<DropdownMenuItem
												onClick={() => navigate({ to: "/workspace/mcp-inspector", search: { target: `endpoint:${e.name}` } })}
											>
												<SearchCode className="h-4 w-4" /> Inspect
											</DropdownMenuItem>
											{canUpdate && (
												<DropdownMenuItem onClick={() => setEditing(e)}>
													<Pencil className="h-4 w-4" /> Edit
												</DropdownMenuItem>
											)}
											{canDelete && (
												<DropdownMenuItem className="text-destructive" onClick={() => setPendingDelete(e)}>
													<Trash2 className="h-4 w-4" /> Delete
												</DropdownMenuItem>
											)}
										</DropdownMenuContent>
									</DropdownMenu>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>

			{editing && (
				<EndpointDialog
					endpoint={editing === "new" ? null : editing}
					defaultNamespaceId={createFor ? Number(createFor) : undefined}
					onClose={closeEditor}
				/>
			)}
			{connectFor && <ConnectDialog endpoint={connectFor} onClose={() => setConnectFor(null)} />}

			<AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete endpoint {pendingDelete?.name}?</AlertDialogTitle>
						<AlertDialogDescription>
							Clients using /mcp/{pendingDelete?.name} stop working. The namespace {pendingDelete?.virtual_mcp_name} is kept.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={async () => {
								if (!pendingDelete) return;
								const target = pendingDelete;
								setPendingDelete(null);
								try {
									await deleteEndpoint(target.id).unwrap();
									toast({ title: `Endpoint ${target.name} deleted` });
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

function EndpointDialog({
	endpoint,
	defaultNamespaceId,
	onClose,
}: {
	endpoint: SBSEndpoint | null;
	defaultNamespaceId?: number;
	onClose: () => void;
}) {
	const { toast } = useToast();
	const { data: namespaces } = useGetVirtualMCPsQuery({ limit: 100, offset: 0 });
	const [createEndpoint, { isLoading: creating }] = useCreateSBSEndpointMutation();
	const [updateEndpoint, { isLoading: updating }] = useUpdateSBSEndpointMutation();

	const [name, setName] = useState(endpoint?.name ?? "");
	const [description, setDescription] = useState(endpoint?.description ?? "");
	const [namespaceId, setNamespaceId] = useState<string>(
		endpoint ? String(endpoint.virtual_mcp_id) : defaultNamespaceId ? String(defaultNamespaceId) : "",
	);
	const [authMode, setAuthMode] = useState<SBSEndpointAuthMode>(endpoint?.auth_mode ?? "virtual_key");
	const [publicKey, setPublicKey] = useState(endpoint?.public_virtual_key_id ?? "");
	const [queryParam, setQueryParam] = useState(endpoint?.allow_query_param_key ?? false);
	const [openapi, setOpenapi] = useState(endpoint?.openapi_enabled ?? true);
	const [enabled, setEnabled] = useState(endpoint?.enabled ?? true);

	const submit = async () => {
		const data: SBSEndpointRequest = {
			name: name.trim(),
			description: description.trim() || undefined,
			virtual_mcp_id: Number(namespaceId),
			enabled,
			auth_mode: authMode,
			public_virtual_key_id: authMode === "public" ? publicKey : undefined,
			allow_query_param_key: queryParam,
			openapi_enabled: openapi,
		};
		try {
			if (endpoint) await updateEndpoint({ id: endpoint.id, data }).unwrap();
			else await createEndpoint(data).unwrap();
			toast({ title: endpoint ? "Endpoint updated" : "Endpoint created" });
			onClose();
		} catch (err) {
			toast({ title: endpoint ? "Update failed" : "Create failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const valid = /^[a-zA-Z0-9_-]{1,64}$/.test(name.trim()) && namespaceId && (authMode !== "public" || publicKey);

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-w-lg">
				<DialogHeader>
					<DialogTitle>{endpoint ? `Edit endpoint ${endpoint.name}` : "Create endpoint"}</DialogTitle>
					<DialogDescription>The endpoint is served at /mcp/&lt;name&gt;.</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-1.5">
						<Label>Name</Label>
						<Input value={name} onChange={(e) => setName(e.target.value)} placeholder="chatbot" className="font-mono" />
						{name && <p className="text-muted-foreground font-mono text-xs">{mcpEndpointUrl(name.trim() || "…")}</p>}
					</div>
					<div className="space-y-1.5">
						<Label>Description</Label>
						<Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={2} />
					</div>
					<div className="space-y-1.5">
						<Label>Namespace</Label>
						<Select value={namespaceId} onValueChange={setNamespaceId}>
							<SelectTrigger>
								<SelectValue placeholder="Choose the namespace to serve" />
							</SelectTrigger>
							<SelectContent>
								{(namespaces?.virtual_mcps ?? []).map((n) => (
									<SelectItem key={n.id} value={String(n.id)}>
										{n.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<div className="space-y-1.5">
						<Label>Authentication</Label>
						<Select value={authMode} onValueChange={(v) => setAuthMode(v as SBSEndpointAuthMode)}>
							<SelectTrigger>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="virtual_key">Virtual key required</SelectItem>
								<SelectItem value="public">Public (no key; acts as a chosen virtual key)</SelectItem>
							</SelectContent>
						</Select>
						{authMode === "virtual_key" && (
							<p className="text-muted-foreground text-xs">
								Callers send a virtual key (x-bf-vk or Authorization: Bearer). The key must have this namespace assigned.
							</p>
						)}
					</div>
					{authMode === "public" && (
						<div className="space-y-1.5">
							<Label>Acts as virtual key</Label>
							<VirtualKeySelector value={publicKey} onChange={setPublicKey} placeholder="Choose a virtual key" noPortal />
							<p className="text-muted-foreground text-xs">
								Anyone who can reach the URL can use the namespace. Its budget, rate limits and logs are this key&apos;s; the namespace is
								assigned to it on save.
							</p>
						</div>
					)}
					<div className="flex items-center justify-between rounded-md border p-3">
						<div>
							<div className="text-sm font-medium">Accept the key in the URL</div>
							<div className="text-muted-foreground text-xs">For clients that cannot set headers: ?api_key=&lt;virtual key&gt;</div>
						</div>
						<Switch checked={queryParam} onCheckedChange={setQueryParam} />
					</div>
					<div className="flex items-center justify-between rounded-md border p-3">
						<div>
							<div className="text-sm font-medium">OpenAPI endpoint</div>
							<div className="text-muted-foreground text-xs">
								Serve the tools as REST at /mcp/&lt;name&gt;/openapi.json (Open WebUI and others)
							</div>
						</div>
						<Switch checked={openapi} onCheckedChange={setOpenapi} />
					</div>
					<div className="flex items-center justify-between rounded-md border p-3">
						<div className="text-sm font-medium">Enabled</div>
						<Switch checked={enabled} onCheckedChange={setEnabled} />
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button onClick={submit} disabled={!valid || creating || updating}>
						{(creating || updating) && <Loader2 className="h-4 w-4 animate-spin" />}
						{endpoint ? "Save" : "Create"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function ConnectDialog({ endpoint, onClose }: { endpoint: SBSEndpoint; onClose: () => void }) {
	const url = mcpEndpointUrl(endpoint.name);
	const needsKey = endpoint.auth_mode !== "public";
	const headers = needsKey ? { Authorization: "Bearer <YOUR_VIRTUAL_KEY>" } : undefined;
	const cursor = JSON.stringify({ mcpServers: { [endpoint.name]: { url, ...(headers && { headers }) } } }, null, 2);
	const claudeCode = `claude mcp add --transport http ${endpoint.name} ${url}${needsKey ? ` --header "Authorization: Bearer <YOUR_VIRTUAL_KEY>"` : ""}`;
	const stdioBridge = JSON.stringify(
		{
			mcpServers: {
				[endpoint.name]: {
					command: "npx",
					args: ["-y", "mcp-remote", url, ...(needsKey ? ["--header", "Authorization: Bearer ${BIFROST_VK}"] : [])],
					...(needsKey && { env: { BIFROST_VK: "<YOUR_VIRTUAL_KEY>" } }),
				},
			},
		},
		null,
		2,
	);
	const curl = `curl -s ${url}/openapi.json${needsKey ? ` -H "x-bf-vk: <YOUR_VIRTUAL_KEY>"` : ""}`;

	const Block = ({ value }: { value: string }) => (
		<div className="relative">
			<pre className="bg-muted max-h-64 overflow-auto rounded-md p-3 pr-10 font-mono text-xs whitespace-pre-wrap">{value}</pre>
			<CopyButton value={value} className="absolute top-1 right-1" />
		</div>
	);

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-w-2xl">
				<DialogHeader>
					<DialogTitle>Connect a client to {endpoint.name}</DialogTitle>
					<DialogDescription>
						{needsKey
							? "Replace <YOUR_VIRTUAL_KEY> with a virtual key that has this namespace assigned."
							: "This endpoint is public: no key needed."}
					</DialogDescription>
				</DialogHeader>
				<Tabs defaultValue="json">
					<TabsList>
						<TabsTrigger value="json">mcp.json (Cursor, VS Code)</TabsTrigger>
						<TabsTrigger value="claude">Claude Code</TabsTrigger>
						<TabsTrigger value="stdio">STDIO-only clients</TabsTrigger>
						{endpoint.openapi_enabled && <TabsTrigger value="openapi">OpenAPI</TabsTrigger>}
					</TabsList>
					<TabsContent value="json">
						<Block value={cursor} />
					</TabsContent>
					<TabsContent value="claude">
						<Block value={claudeCode} />
					</TabsContent>
					<TabsContent value="stdio">
						<p className="text-muted-foreground mb-2 text-xs">For Claude Desktop and other clients that only start local processes.</p>
						<Block value={stdioBridge} />
					</TabsContent>
					{endpoint.openapi_enabled && (
						<TabsContent value="openapi">
							<p className="text-muted-foreground mb-2 text-xs">
								In Open WebUI: Settings → Tools → add {url}/openapi.json{needsKey ? " with the virtual key as the bearer token" : ""}.
							</p>
							<Block value={curl} />
						</TabsContent>
					)}
				</Tabs>
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Close
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
