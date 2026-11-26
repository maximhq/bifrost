// SBS fork: MetaMCP-style MCP server detail page.
import MCPClientSheet from "@/app/workspace/mcp-registry/views/mcpClientSheet";
import { ConnectionStateBadge, ConnectionTypeBadge, CopyField, InfoCard, SectionHeader, StatusBadge } from "@/components/sbs/mcpShared";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/hooks/use-toast";
import { mcpEndpointUrl } from "@/lib/sbs/mcpClient";
import { gatewayToolName, isServerActive, isToolActive, specFor } from "@/lib/sbs/namespace";
import {
	getErrorMessage,
	useGetMCPClientsQuery,
	useGetVirtualMCPsQuery,
	useReconnectMCPClientMutation,
	useRefreshMCPClientToolsMutation,
} from "@/lib/store";
import type { MCPClient } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { Loader2, Pencil, PlugZap, RefreshCw, Search, SearchCode } from "lucide-react";
import { useState } from "react";

function connectionTarget(c: MCPClient): string {
	const cfg = c.config;
	if (cfg.connection_type === "stdio" && cfg.stdio_config) {
		return [cfg.stdio_config.command, ...(cfg.stdio_config.args ?? [])].join(" ");
	}
	if (cfg.connection_type === "openapi") {
		return cfg.openapi_config?.base_url || "OpenAPI document";
	}
	const conn = cfg.connection_string;
	if (conn?.ref) return `env.${conn.ref}`;
	return conn?.value || "—";
}

export default function MCPServerDetailPage({ id }: { id: string }) {
	const { toast } = useToast();
	const canUpdate = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const { data, isLoading, refetch } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const { data: namespaces } = useGetVirtualMCPsQuery({ limit: 100, offset: 0 });
	const [reconnect, { isLoading: reconnecting }] = useReconnectMCPClientMutation();
	const [refreshTools, { isLoading: refreshing }] = useRefreshMCPClientToolsMutation();
	const [editing, setEditing] = useState(false);
	const [toolSearch, setToolSearch] = useState("");

	const client = data?.clients.find((c) => c.config.client_id === id);

	if (isLoading) {
		return (
			<div className="flex grow items-center justify-center">
				<Loader2 className="h-6 w-6 animate-spin" />
			</div>
		);
	}
	if (!client) {
		return (
			<div className="p-6">
				<PageTitle breadcrumbs={[{ label: "MCP servers", to: "/workspace/mcp-registry" }, { label: "Not found" }]} />
				<p className="text-muted-foreground text-sm">No MCP server with ID {id}.</p>
			</div>
		);
	}

	const cfg = client.config;
	const tools = client.tools ?? [];
	const exposed = new Set(cfg.tools_to_execute ?? []);
	const allExposed = exposed.has("*");
	const usedIn = (namespaces?.virtual_mcps ?? []).filter((n) => specFor(n.tools, cfg.client_id));
	const filteredTools = tools.filter((t) => {
		const q = toolSearch.trim().toLowerCase();
		return !q || t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q);
	});

	return (
		<div className="flex grow flex-col gap-4 overflow-auto pb-6">
			<PageTitle
				breadcrumbs={[{ label: "MCP servers", to: "/workspace/mcp-registry" }, { label: cfg.name }]}
				tags={[{ label: client.state, tone: client.state === "healthy" ? "success" : client.state === "disabled" ? "neutral" : "warning" }]}
			/>

			<div className="flex flex-wrap items-center gap-2">
				{canUpdate && (
					<Button variant="outline" size="sm" onClick={() => setEditing(true)}>
						<Pencil className="h-4 w-4" /> Edit
					</Button>
				)}
				<Button
					variant="outline"
					size="sm"
					disabled={reconnecting || !canUpdate}
					onClick={async () => {
						try {
							await reconnect(cfg.client_id).unwrap();
							toast({ title: `${cfg.name} reconnected` });
						} catch (err) {
							toast({ title: "Reconnect failed", description: getErrorMessage(err), variant: "destructive" });
						}
					}}
				>
					<PlugZap className="h-4 w-4" /> Reconnect
				</Button>
				<Button
					variant="outline"
					size="sm"
					disabled={refreshing || !canUpdate}
					onClick={async () => {
						try {
							const res = await refreshTools(cfg.client_id).unwrap();
							toast({ title: `Tools refreshed: ${res.tool_count}` });
							refetch();
						} catch (err) {
							toast({ title: "Refresh failed", description: getErrorMessage(err), variant: "destructive" });
						}
					}}
				>
					<RefreshCw className={`h-4 w-4 ${refreshing ? "animate-spin" : ""}`} /> Refresh tools
				</Button>
				{cfg.endpoint_slug && (
					<Button variant="outline" size="sm" asChild>
						<Link to="/workspace/mcp-inspector" search={{ target: `server:${cfg.endpoint_slug}` }}>
							<SearchCode className="h-4 w-4" /> Inspect
						</Link>
					</Button>
				)}
			</div>

			{client.last_failure && (
				<div className="border-destructive/50 bg-destructive/5 rounded-md border p-3 text-sm">
					<div className="font-medium">Last failure ({client.last_failure.stage})</div>
					<div className="text-muted-foreground break-all">{client.last_failure.message}</div>
				</div>
			)}

			<div className="grid gap-4 md:grid-cols-2">
				<InfoCard
					title="Basic information"
					rows={[
						{ label: "Type", value: <ConnectionTypeBadge type={cfg.connection_type} /> },
						{ label: cfg.connection_type === "stdio" ? "Command" : "URL", value: <span className="font-mono text-xs">{connectionTarget(client)}</span> },
						{ label: "Auth", value: cfg.auth_type ?? "none" },
						{ label: "Connection", value: <ConnectionStateBadge state={client.state} /> },
						{ label: "Code mode", value: cfg.is_code_mode_client ? "on" : "off" },
						{ label: "Client ID", value: <span className="font-mono text-xs">{cfg.client_id}</span> },
					]}
				/>
				<InfoCard
					title="Statistics"
					rows={[
						{ label: "Tools discovered", value: tools.length },
						{ label: "Exposed by the gateway", value: allExposed ? `all (${tools.length})` : exposed.size },
						{ label: "Prompts", value: client.prompts?.length ?? 0 },
						{ label: "Resources", value: (client.resources?.length ?? 0) + (client.resource_templates?.length ?? 0) },
						{ label: "Namespaces", value: usedIn.length },
						{
							label: "Direct URL",
							value: cfg.endpoint_slug ? <CopyField value={mcpEndpointUrl(cfg.endpoint_slug)} /> : "—",
						},
					]}
				/>
			</div>

			<Tabs defaultValue="tools">
				<TabsList>
					<TabsTrigger value="tools">Tools ({tools.length})</TabsTrigger>
					<TabsTrigger value="prompts">Prompts ({client.prompts?.length ?? 0})</TabsTrigger>
					<TabsTrigger value="resources">Resources ({(client.resources?.length ?? 0) + (client.resource_templates?.length ?? 0)})</TabsTrigger>
					<TabsTrigger value="namespaces">Namespaces ({usedIn.length})</TabsTrigger>
				</TabsList>

				<TabsContent value="tools" className="mt-4">
					<SectionHeader title="Tools" description="Discovered from the server. 'Gateway' is the server-level allow list (Edit → Tools); namespaces narrow it further.">
						<div className="relative w-64">
							<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
							<Input placeholder="Search tools" value={toolSearch} onChange={(e) => setToolSearch(e.target.value)} className="h-8 pl-9" />
						</div>
					</SectionHeader>
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead>Tool</TableHead>
									<TableHead>Description</TableHead>
									<TableHead>Hints</TableHead>
									<TableHead>Gateway</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{filteredTools.length === 0 && (
									<TableRow>
										<TableCell colSpan={4} className="text-muted-foreground py-8 text-center text-sm">
											No tools discovered.
										</TableCell>
									</TableRow>
								)}
								{filteredTools.map((t) => (
									<TableRow key={t.name}>
										<TableCell>
											<div className="font-mono text-sm">{t.name}</div>
											<div className="text-muted-foreground font-mono text-xs">{gatewayToolName(cfg.name, t.name)}</div>
										</TableCell>
										<TableCell className="text-muted-foreground max-w-xl text-sm">
											<div className="line-clamp-3">{t.description || "—"}</div>
										</TableCell>
										<TableCell>
											<div className="flex flex-wrap gap-1">
												{t.annotations?.readOnlyHint && <Badge variant="outline">read-only</Badge>}
												{t.annotations?.destructiveHint && <Badge variant="destructive">destructive</Badge>}
												{t.annotations?.idempotentHint && <Badge variant="outline">idempotent</Badge>}
												{t.annotations?.openWorldHint && <Badge variant="outline">open world</Badge>}
											</div>
										</TableCell>
										<TableCell>
											<StatusBadge active={allExposed || exposed.has(t.name)} activeLabel="Exposed" inactiveLabel="Hidden" />
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</TabsContent>

				<TabsContent value="prompts" className="mt-4">
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead>Prompt</TableHead>
									<TableHead>Description</TableHead>
									<TableHead>Arguments</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{(client.prompts ?? []).length === 0 && (
									<TableRow>
										<TableCell colSpan={3} className="text-muted-foreground py-8 text-center text-sm">
											No prompts.
										</TableCell>
									</TableRow>
								)}
								{(client.prompts ?? []).map((p) => (
									<TableRow key={p.name}>
										<TableCell>
											<div className="font-mono text-sm">{p.name}</div>
											<div className="text-muted-foreground font-mono text-xs">{gatewayToolName(cfg.name, p.name)}</div>
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{p.description || "—"}</TableCell>
										<TableCell className="font-mono text-xs">{(p.arguments ?? []).map((a) => (a.required ? `${a.name}*` : a.name)).join(", ") || "—"}</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</TabsContent>

				<TabsContent value="resources" className="mt-4">
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead>URI</TableHead>
									<TableHead>Name</TableHead>
									<TableHead>Type</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{(client.resources ?? []).length + (client.resource_templates ?? []).length === 0 && (
									<TableRow>
										<TableCell colSpan={3} className="text-muted-foreground py-8 text-center text-sm">
											No resources.
										</TableCell>
									</TableRow>
								)}
								{(client.resources ?? []).map((r) => (
									<TableRow key={r.uri}>
										<TableCell className="font-mono text-xs">{r.uri}</TableCell>
										<TableCell className="text-sm">{r.title || r.name}</TableCell>
										<TableCell className="text-muted-foreground text-xs">{r.mimeType || "resource"}</TableCell>
									</TableRow>
								))}
								{(client.resource_templates ?? []).map((r) => (
									<TableRow key={r.uriTemplate}>
										<TableCell className="font-mono text-xs">{r.uriTemplate}</TableCell>
										<TableCell className="text-sm">{r.name}</TableCell>
										<TableCell className="text-muted-foreground text-xs">template</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</TabsContent>

				<TabsContent value="namespaces" className="mt-4">
					<div className="rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead>Namespace</TableHead>
									<TableHead>Server status</TableHead>
									<TableHead>Active tools here</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{usedIn.length === 0 && (
									<TableRow>
										<TableCell colSpan={3} className="text-muted-foreground py-8 text-center text-sm">
											Not part of any namespace yet.
										</TableCell>
									</TableRow>
								)}
								{usedIn.map((n) => {
									const spec = specFor(n.tools, cfg.client_id);
									const active = tools.filter((t) => isServerActive(spec) && isToolActive(spec, t.name)).length;
									return (
										<TableRow key={n.id}>
											<TableCell>
												<Link to="/workspace/mcp-namespaces/$id" params={{ id: String(n.id) }} className="font-medium hover:underline">
													{n.name}
												</Link>
											</TableCell>
											<TableCell>
												<StatusBadge active={isServerActive(spec)} />
											</TableCell>
											<TableCell>
												{active}/{tools.length}
											</TableCell>
										</TableRow>
									);
								})}
							</TableBody>
						</Table>
					</div>
				</TabsContent>
			</Tabs>

			{editing && (
				<MCPClientSheet
					mcpClient={client}
					onClose={() => setEditing(false)}
					onSubmitSuccess={() => {
						setEditing(false);
						refetch();
					}}
				/>
			)}
		</div>
	);
}
