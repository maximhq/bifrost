// SBS fork: MetaMCP-style "Namespaces" list. A namespace is a Bifrost Virtual MCP: a group of MCP
// servers whose tools can be switched on and off and presented differently, served by endpoints.
import { StatusBadge, formatDate } from "@/components/sbs/mcpShared";
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
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { isServerActive, TOOL_WILDCARD } from "@/lib/sbs/namespace";
import {
	getErrorMessage,
	useCreateVirtualMCPMutation,
	useDeleteVirtualMCPMutation,
	useGetMCPClientsQuery,
	useGetSBSEndpointsQuery,
	useGetVirtualMCPsQuery,
	useUpdateVirtualMCPMutation,
} from "@/lib/store";
import type { VirtualMCP } from "@/lib/types/virtualMcps";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useNavigate } from "@tanstack/react-router";
import { Loader2, MoreHorizontal, Plus, Search, Trash2, ExternalLink } from "lucide-react";
import { useMemo, useState } from "react";

export default function NamespacesPage() {
	const navigate = useNavigate();
	const { toast } = useToast();
	const [search, setSearch] = useState("");
	const [creating, setCreating] = useState(false);
	const [pendingDelete, setPendingDelete] = useState<VirtualMCP | null>(null);
	const canCreate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.VirtualMCPs, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.VirtualMCPs, RbacOperation.Delete);

	const { data, isLoading, isFetching } = useGetVirtualMCPsQuery({ limit: 100, offset: 0 });
	const { data: endpoints } = useGetSBSEndpointsQuery();
	const { data: clientsData } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const [updateVirtualMCP] = useUpdateVirtualMCPMutation();
	const [deleteVirtualMCP, { isLoading: deleting }] = useDeleteVirtualMCPMutation();

	const clients = clientsData?.clients ?? [];
	const toolCount = useMemo(() => new Map(clients.map((c) => [c.config.client_id, c.tools?.length ?? 0])), [clients]);

	const rows = (data?.virtual_mcps ?? []).filter((n) => {
		const q = search.trim().toLowerCase();
		return !q || n.name.toLowerCase().includes(q) || (n.description ?? "").toLowerCase().includes(q) || n.endpoint_slug.includes(q);
	});

	const activeToolCount = (n: VirtualMCP) =>
		n.tools.reduce(
			(sum, spec) => sum + (spec.tool_names.includes(TOOL_WILDCARD) ? (toolCount.get(spec.mcp_client_id) ?? 0) : spec.tool_names.length),
			0,
		);

	const toggleEnabled = async (n: VirtualMCP, enabled: boolean) => {
		try {
			await updateVirtualMCP({ id: n.id, data: { enabled } }).unwrap();
			toast({ title: enabled ? `Namespace ${n.name} enabled` : `Namespace ${n.name} disabled` });
		} catch (err) {
			toast({ title: "Update failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	const confirmDelete = async () => {
		if (!pendingDelete) return;
		const target = pendingDelete;
		setPendingDelete(null);
		try {
			await deleteVirtualMCP(target.id).unwrap();
			toast({ title: `Namespace ${target.name} deleted` });
		} catch (err) {
			toast({ title: "Delete failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	return (
		<div className="flex grow flex-col overflow-auto">
			<PageTitle title="Namespaces">
				Group MCP servers into a namespace, choose which of their tools it serves and how they are presented, then serve it through an
				endpoint.
			</PageTitle>

			<div className="mb-4 flex items-center justify-between gap-3">
				<div className="relative max-w-sm min-w-[200px] flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input placeholder="Search namespaces" value={search} onChange={(e) => setSearch(e.target.value)} className="pl-9" />
				</div>
				{canCreate && (
					<Button onClick={() => setCreating(true)}>
						<Plus className="h-4 w-4" />
						Create namespace
					</Button>
				)}
			</div>

			<div className={`grow overflow-auto rounded-sm border ${isFetching ? "opacity-70 transition-opacity" : ""}`}>
				<Table>
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Description</TableHead>
							<TableHead>Servers</TableHead>
							<TableHead>Active tools</TableHead>
							<TableHead>Endpoints</TableHead>
							<TableHead>Enabled</TableHead>
							<TableHead>Created</TableHead>
							<TableHead className="w-10" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading && (
							<TableRow>
								<TableCell colSpan={8} className="py-10 text-center">
									<Loader2 className="mx-auto h-5 w-5 animate-spin" />
								</TableCell>
							</TableRow>
						)}
						{!isLoading && rows.length === 0 && (
							<TableRow>
								<TableCell colSpan={8} className="text-muted-foreground py-10 text-center text-sm">
									{search ? "No namespace matches the search." : "No namespaces yet. Create one to group MCP servers."}
								</TableCell>
							</TableRow>
						)}
						{rows.map((n) => {
							const active = n.tools.filter((s) => isServerActive(s)).length;
							const eps = (endpoints ?? []).filter((e) => e.virtual_mcp_id === n.id);
							return (
								<TableRow
									key={n.id}
									className="hover:bg-muted/50 cursor-pointer"
									onClick={() => navigate({ to: "/workspace/mcp-namespaces/$id", params: { id: String(n.id) } })}
								>
									<TableCell className="font-medium">
										<div>{n.name}</div>
										<div className="text-muted-foreground font-mono text-xs">/mcp/{n.endpoint_slug}</div>
									</TableCell>
									<TableCell className="text-muted-foreground max-w-xs truncate">{n.description || "—"}</TableCell>
									<TableCell>
										{active}/{n.tools.length}
									</TableCell>
									<TableCell>{activeToolCount(n)}</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-1">
											{eps.length === 0 && <span className="text-muted-foreground">—</span>}
											{eps.map((e) => (
												<Badge key={e.id} variant="outline" className="font-mono">
													{e.name}
												</Badge>
											))}
										</div>
									</TableCell>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<Switch checked={n.enabled} disabled={!canUpdate} onCheckedChange={(v) => toggleEnabled(n, v)} />
									</TableCell>
									<TableCell className="text-muted-foreground text-xs">{formatDate(n.created_at)}</TableCell>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<DropdownMenu>
											<DropdownMenuTrigger asChild>
												<Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Actions">
													<MoreHorizontal className="h-4 w-4" />
												</Button>
											</DropdownMenuTrigger>
											<DropdownMenuContent align="end">
												<DropdownMenuItem onClick={() => navigate({ to: "/workspace/mcp-namespaces/$id", params: { id: String(n.id) } })}>
													<ExternalLink className="h-4 w-4" /> Open
												</DropdownMenuItem>
												{canDelete && (
													<DropdownMenuItem className="text-destructive" disabled={deleting} onClick={() => setPendingDelete(n)}>
														<Trash2 className="h-4 w-4" /> Delete
													</DropdownMenuItem>
												)}
											</DropdownMenuContent>
										</DropdownMenu>
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>

			{creating && <CreateNamespaceDialog onClose={() => setCreating(false)} />}

			<AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete namespace {pendingDelete?.name}?</AlertDialogTitle>
						<AlertDialogDescription>
							It stops being served at /mcp/{pendingDelete?.endpoint_slug}, endpoints pointing at it stop answering, and it is removed from
							the virtual keys it is assigned to. The MCP servers themselves are kept.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={confirmDelete}>Delete</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}

function CreateNamespaceDialog({ onClose }: { onClose: () => void }) {
	const navigate = useNavigate();
	const { toast } = useToast();
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [selected, setSelected] = useState<Set<string>>(new Set());
	const { data: clientsData } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const [createVirtualMCP, { isLoading }] = useCreateVirtualMCPMutation();
	const clients = clientsData?.clients ?? [];

	const submit = async () => {
		try {
			const created = await createVirtualMCP({
				name: name.trim(),
				description: description.trim() || undefined,
				enabled: true,
				tools: Array.from(selected).map((id) => ({ mcp_client_id: id, tool_names: [TOOL_WILDCARD] })),
			}).unwrap();
			toast({ title: `Namespace ${created.name} created` });
			onClose();
			navigate({ to: "/workspace/mcp-namespaces/$id", params: { id: String(created.id) } });
		} catch (err) {
			toast({ title: "Create failed", description: getErrorMessage(err), variant: "destructive" });
		}
	};

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-w-lg">
				<DialogHeader>
					<DialogTitle>Create namespace</DialogTitle>
					<DialogDescription>
						Pick the MCP servers to group. All their tools start active; refine them on the namespace page.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-1.5">
						<Label htmlFor="ns-name">Name</Label>
						<Input id="ns-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="soc-tools" autoFocus />
						<p className="text-muted-foreground text-xs">Also served directly at /mcp/&lt;slug derived from the name&gt;.</p>
					</div>
					<div className="space-y-1.5">
						<Label htmlFor="ns-desc">Description</Label>
						<Textarea id="ns-desc" value={description} onChange={(e) => setDescription(e.target.value)} rows={2} />
					</div>
					<div className="space-y-1.5">
						<Label>MCP servers</Label>
						<div className="max-h-64 space-y-1 overflow-auto rounded-md border p-2">
							{clients.length === 0 && <p className="text-muted-foreground p-2 text-sm">No MCP servers configured yet.</p>}
							{clients.map((c) => (
								<label
									key={c.config.client_id}
									className="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm"
								>
									<Checkbox
										checked={selected.has(c.config.client_id)}
										onCheckedChange={(v) => {
											const next = new Set(selected);
											if (v) next.add(c.config.client_id);
											else next.delete(c.config.client_id);
											setSelected(next);
										}}
									/>
									<span className="font-medium">{c.config.name}</span>
									<span className="text-muted-foreground ml-auto text-xs">{c.tools?.length ?? 0} tools</span>
								</label>
							))}
						</div>
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button onClick={submit} disabled={!name.trim() || isLoading}>
						{isLoading && <Loader2 className="h-4 w-4 animate-spin" />}
						Create
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
