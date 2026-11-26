// SBS fork: MetaMCP-style MCP Inspector. Connects from the browser to this Bifrost's own
// /mcp/<slug> (an endpoint, a namespace or a single server) with a virtual key, exactly as an
// MCP client would, so what you see is what clients get: auth, governance, overrides and logs apply.
import { SectionHeader } from "@/components/sbs/mcpShared";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
	BrowserMCPClient,
	exampleArgs,
	mcpEndpointUrl,
	type MCPPrompt,
	type MCPResource,
	type MCPResourceTemplate,
	type MCPTool,
	type MCPTraffic,
} from "@/lib/sbs/mcpClient";
import { useGetMCPClientsQuery, useGetSBSEndpointsQuery, useGetVirtualKeysQuery, useGetVirtualMCPsQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { Loader2, Play, Plug, PlugZap, RefreshCw, Search, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

const VK_STORAGE_KEY = "sbs.inspector.vk";

function loadStoredKey(): string {
	try {
		return localStorage.getItem(VK_STORAGE_KEY) ?? "";
	} catch {
		return "";
	}
}

function storeKey(value: string) {
	try {
		if (value) localStorage.setItem(VK_STORAGE_KEY, value);
		else localStorage.removeItem(VK_STORAGE_KEY);
	} catch {
		// storage unavailable: the key is just not remembered
	}
}

function pretty(value: unknown): string {
	if (typeof value === "string") return value;
	try {
		return JSON.stringify(value, null, 2);
	} catch {
		return String(value);
	}
}

function slugOf(target: string): string {
	return target.slice(target.indexOf(":") + 1);
}

type Status = "idle" | "connecting" | "connected" | "error";

export default function MCPInspectorPage({ initialTarget }: { initialTarget?: string }) {
	const { data: endpoints } = useGetSBSEndpointsQuery();
	const { data: namespaces } = useGetVirtualMCPsQuery({ limit: 100, offset: 0 });
	const { data: clientsData } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const { data: keys } = useGetVirtualKeysQuery({ limit: 100, offset: 0 });

	const [target, setTarget] = useState(initialTarget ?? "");
	const [virtualKey, setVirtualKey] = useState(loadStoredKey);
	const [status, setStatus] = useState<Status>("idle");
	const [error, setError] = useState<string | null>(null);
	const [traffic, setTraffic] = useState<MCPTraffic[]>([]);
	const clientRef = useRef<BrowserMCPClient | null>(null);
	const [info, setInfo] = useState<{
		name?: string;
		version?: string;
		capabilities?: Record<string, unknown>;
		instructions?: string;
	} | null>(null);

	const [tools, setTools] = useState<MCPTool[]>([]);
	const [prompts, setPrompts] = useState<MCPPrompt[]>([]);
	const [resources, setResources] = useState<MCPResource[]>([]);
	const [templates, setTemplates] = useState<MCPResourceTemplate[]>([]);

	const publicEndpoint = useMemo(() => {
		if (!target.startsWith("endpoint:")) return false;
		return (endpoints ?? []).some((e) => e.name === slugOf(target) && e.auth_mode === "public");
	}, [target, endpoints]);

	useEffect(() => {
		return () => {
			clientRef.current?.close();
		};
	}, []);

	const disconnect = async () => {
		await clientRef.current?.close();
		clientRef.current = null;
		setStatus("idle");
		setInfo(null);
		setTools([]);
		setPrompts([]);
		setResources([]);
		setTemplates([]);
	};

	const refreshLists = async (client: BrowserMCPClient) => {
		const caps = client.capabilities ?? {};
		const [t, p, r, rt] = await Promise.all([
			client.listTools().catch(() => []),
			caps.prompts ? client.listPrompts().catch(() => []) : Promise.resolve([]),
			caps.resources ? client.listResources().catch(() => []) : Promise.resolve([]),
			caps.resources ? client.listResourceTemplates().catch(() => []) : Promise.resolve([]),
		]);
		setTools(t);
		setPrompts(p);
		setResources(r);
		setTemplates(rt);
	};

	const connect = async () => {
		await disconnect();
		if (!target) return;
		setStatus("connecting");
		setError(null);
		storeKey(virtualKey);
		const client = new BrowserMCPClient(slugOf(target), virtualKey.trim(), (entry) => setTraffic((prev) => [entry, ...prev].slice(0, 200)));
		try {
			await client.connect();
			clientRef.current = client;
			setInfo({ ...client.serverInfo, capabilities: client.capabilities, instructions: client.instructions });
			await refreshLists(client);
			setStatus("connected");
		} catch (e) {
			setStatus("error");
			setError((e as Error).message);
		}
	};

	const selectedKeyId = (keys?.virtual_keys ?? []).find((k) => k.value === virtualKey)?.id ?? "";

	return (
		<div className="flex grow flex-col gap-4 overflow-auto pb-6">
			<PageTitle title="MCP Inspector">
				Connect to an endpoint, namespace or server the way an MCP client does, and try its tools, prompts and resources.
			</PageTitle>

			<div className="grid gap-3 rounded-md border p-4 md:grid-cols-[2fr_2fr_auto]">
				<div className="space-y-1.5">
					<Label>Target</Label>
					<Select value={target} onValueChange={setTarget}>
						<SelectTrigger>
							<SelectValue placeholder="Choose what to connect to" />
						</SelectTrigger>
						<SelectContent>
							<SelectGroup>
								<SelectLabel>Endpoints</SelectLabel>
								{(endpoints ?? []).map((e) => (
									<SelectItem key={`endpoint:${e.name}`} value={`endpoint:${e.name}`}>
										{e.name} → {e.virtual_mcp_name}
									</SelectItem>
								))}
							</SelectGroup>
							<SelectGroup>
								<SelectLabel>Namespaces</SelectLabel>
								{(namespaces?.virtual_mcps ?? []).map((n) => (
									<SelectItem key={`vmcp:${n.endpoint_slug}`} value={`vmcp:${n.endpoint_slug}`}>
										{n.name}
									</SelectItem>
								))}
							</SelectGroup>
							<SelectGroup>
								<SelectLabel>MCP servers</SelectLabel>
								{(clientsData?.clients ?? [])
									.filter((c) => c.config.endpoint_slug)
									.map((c) => (
										<SelectItem key={`server:${c.config.endpoint_slug}`} value={`server:${c.config.endpoint_slug}`}>
											{c.config.name}
										</SelectItem>
									))}
							</SelectGroup>
						</SelectContent>
					</Select>
					{target && <p className="text-muted-foreground font-mono text-xs">{mcpEndpointUrl(slugOf(target))}</p>}
				</div>
				<div className="space-y-1.5">
					<Label>
						Virtual key {publicEndpoint && <span className="text-muted-foreground font-normal">(optional: public endpoint)</span>}
					</Label>
					<div className="flex gap-2">
						<Select
							value={selectedKeyId}
							onValueChange={(id) => setVirtualKey((keys?.virtual_keys ?? []).find((k) => k.id === id)?.value ?? "")}
						>
							<SelectTrigger className="w-44 shrink-0">
								<SelectValue placeholder="Pick a key" />
							</SelectTrigger>
							<SelectContent>
								{(keys?.virtual_keys ?? []).map((k) => (
									<SelectItem key={k.id} value={k.id}>
										{k.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Input
							type="password"
							value={virtualKey}
							onChange={(e) => setVirtualKey(e.target.value)}
							placeholder="or paste sk-bf-…"
							className="font-mono"
						/>
					</div>
				</div>
				<div className="flex items-end gap-2">
					{status === "connected" ? (
						<>
							<Button variant="outline" onClick={() => clientRef.current && refreshLists(clientRef.current)}>
								<RefreshCw className="h-4 w-4" /> Refresh
							</Button>
							<Button variant="outline" onClick={disconnect}>
								<Plug className="h-4 w-4" /> Disconnect
							</Button>
						</>
					) : (
						<Button onClick={connect} disabled={!target || status === "connecting"}>
							{status === "connecting" ? <Loader2 className="h-4 w-4 animate-spin" /> : <PlugZap className="h-4 w-4" />}
							Connect
						</Button>
					)}
				</div>
			</div>

			{status === "error" && error && (
				<div className="border-destructive/50 bg-destructive/5 rounded-md border p-3 text-sm">
					<div className="font-medium">Connection failed</div>
					<div className="text-muted-foreground break-all">{error}</div>
					<div className="text-muted-foreground mt-1 text-xs">
						A namespace or endpoint is reachable only with a virtual key it is assigned to (Virtual keys → MCP), unless the endpoint is
						public.
					</div>
				</div>
			)}

			{status === "connected" && info && (
				<div className="flex flex-wrap items-center gap-2 text-sm">
					<Badge variant="success">Connected</Badge>
					<span className="font-medium">{info.name}</span>
					{info.version && (
						<span className="text-muted-foreground">{info.version.startsWith("v") ? info.version : `v${info.version}`}</span>
					)}
					{Object.keys(info.capabilities ?? {}).map((cap) => (
						<Badge key={cap} variant="outline">
							{cap}
						</Badge>
					))}
				</div>
			)}

			<Tabs defaultValue="tools">
				<TabsList>
					<TabsTrigger value="tools">Tools ({tools.length})</TabsTrigger>
					<TabsTrigger value="prompts">Prompts ({prompts.length})</TabsTrigger>
					<TabsTrigger value="resources">Resources ({resources.length + templates.length})</TabsTrigger>
					<TabsTrigger value="info">Server info</TabsTrigger>
					<TabsTrigger value="history">History ({traffic.length})</TabsTrigger>
				</TabsList>
				<TabsContent value="tools" className="mt-4">
					<ToolsPanel tools={tools} client={clientRef.current} connected={status === "connected"} />
				</TabsContent>
				<TabsContent value="prompts" className="mt-4">
					<PromptsPanel prompts={prompts} client={clientRef.current} connected={status === "connected"} />
				</TabsContent>
				<TabsContent value="resources" className="mt-4">
					<ResourcesPanel resources={resources} templates={templates} client={clientRef.current} connected={status === "connected"} />
				</TabsContent>
				<TabsContent value="info" className="mt-4">
					<SectionHeader title="Server info">
						<Button
							variant="outline"
							size="sm"
							disabled={status !== "connected"}
							onClick={() => clientRef.current?.ping().catch(() => undefined)}
						>
							Ping
						</Button>
					</SectionHeader>
					<pre className="bg-muted overflow-auto rounded-md p-3 font-mono text-xs">{info ? pretty(info) : "Not connected."}</pre>
				</TabsContent>
				<TabsContent value="history" className="mt-4">
					<SectionHeader title="JSON-RPC history" description="Newest first; the last 200 messages.">
						<Button variant="outline" size="sm" onClick={() => setTraffic([])}>
							<Trash2 className="h-4 w-4" /> Clear
						</Button>
					</SectionHeader>
					<div className="space-y-2">
						{traffic.map((entry, i) => (
							<details key={`${entry.at}-${i}`} className="rounded-md border">
								<summary className="flex cursor-pointer items-center gap-2 px-3 py-2 text-sm">
									<Badge variant={entry.direction === "error" ? "destructive" : entry.direction === "request" ? "outline" : "success"}>
										{entry.direction}
									</Badge>
									<span className="font-mono">{entry.method}</span>
									<span className="text-muted-foreground ml-auto text-xs">
										{new Date(entry.at).toLocaleTimeString()}
										{entry.ms !== undefined && ` · ${Math.round(entry.ms)} ms`}
									</span>
								</summary>
								<pre className="bg-muted max-h-96 overflow-auto p-3 font-mono text-xs">{pretty(entry.payload)}</pre>
							</details>
						))}
						{traffic.length === 0 && <p className="text-muted-foreground text-sm">Nothing yet.</p>}
					</div>
				</TabsContent>
			</Tabs>
		</div>
	);
}

function ResultView({ result, error }: { result: unknown; error: string | null }) {
	if (error)
		return (
			<pre className="border-destructive/50 bg-destructive/5 overflow-auto rounded-md border p-3 font-mono text-xs whitespace-pre-wrap">
				{error}
			</pre>
		);
	if (result === undefined) return null;
	const content = (result as { content?: { type: string; text?: string }[]; isError?: boolean })?.content;
	const isError = (result as { isError?: boolean })?.isError;
	return (
		<div className="space-y-2">
			{isError && <Badge variant="destructive">Tool reported an error</Badge>}
			{Array.isArray(content) &&
				content
					.filter((c) => c.type === "text" && c.text)
					.map((c, i) => (
						<pre key={i} className="bg-muted max-h-96 overflow-auto rounded-md p-3 font-mono text-xs whitespace-pre-wrap">
							{c.text}
						</pre>
					))}
			<details>
				<summary className="text-muted-foreground cursor-pointer text-xs">Raw result</summary>
				<pre className="bg-muted max-h-96 overflow-auto rounded-md p-3 font-mono text-xs">{pretty(result)}</pre>
			</details>
		</div>
	);
}

function ToolsPanel({ tools, client, connected }: { tools: MCPTool[]; client: BrowserMCPClient | null; connected: boolean }) {
	const [search, setSearch] = useState("");
	const [selected, setSelected] = useState<MCPTool | null>(null);
	const [args, setArgs] = useState("{}");
	const [running, setRunning] = useState(false);
	const [result, setResult] = useState<unknown>(undefined);
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		setSelected(null);
		setResult(undefined);
		setError(null);
	}, [tools]);

	const pick = (tool: MCPTool) => {
		setSelected(tool);
		setArgs(JSON.stringify(exampleArgs(tool.inputSchema), null, 2));
		setResult(undefined);
		setError(null);
	};

	const run = async () => {
		if (!client || !selected) return;
		let parsed: Record<string, unknown>;
		try {
			parsed = JSON.parse(args || "{}");
		} catch (e) {
			setError(`Arguments are not valid JSON: ${(e as Error).message}`);
			return;
		}
		setRunning(true);
		setError(null);
		setResult(undefined);
		try {
			setResult(await client.callTool(selected.name, parsed));
		} catch (e) {
			setError((e as Error).message);
		} finally {
			setRunning(false);
		}
	};

	if (!connected) return <p className="text-muted-foreground text-sm">Connect to list tools.</p>;
	const filtered = tools.filter((t) => {
		const q = search.trim().toLowerCase();
		return !q || t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q);
	});

	return (
		<div className="grid gap-4 md:grid-cols-[320px_1fr]">
			<div className="space-y-2">
				<div className="relative">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input placeholder="Search tools" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 pl-9" />
				</div>
				<div className="max-h-[560px] space-y-1 overflow-auto rounded-md border p-1">
					{filtered.map((t) => (
						<button
							key={t.name}
							type="button"
							onClick={() => pick(t)}
							className={cn("hover:bg-muted w-full rounded px-2 py-1.5 text-left", selected?.name === t.name && "bg-muted")}
						>
							<div className="truncate font-mono text-xs">{t.name}</div>
							{t.description && <div className="text-muted-foreground line-clamp-1 text-xs">{t.description}</div>}
						</button>
					))}
					{filtered.length === 0 && <p className="text-muted-foreground p-2 text-sm">No tools.</p>}
				</div>
			</div>
			<div className="space-y-3">
				{!selected && <p className="text-muted-foreground text-sm">Pick a tool.</p>}
				{selected && (
					<>
						<div>
							<div className="font-mono text-sm font-medium">{selected.name}</div>
							{selected.title && <div className="text-sm">{selected.title}</div>}
							<p className="text-muted-foreground text-sm whitespace-pre-wrap">{selected.description}</p>
							<div className="mt-1 flex flex-wrap gap-1">
								{Object.entries(selected.annotations ?? {})
									.filter(([, v]) => v === true)
									.map(([k]) => (
										<Badge key={k} variant="outline">
											{k}
										</Badge>
									))}
							</div>
						</div>
						<details>
							<summary className="text-muted-foreground cursor-pointer text-xs">Input schema</summary>
							<pre className="bg-muted max-h-64 overflow-auto rounded-md p-3 font-mono text-xs">{pretty(selected.inputSchema)}</pre>
						</details>
						<div className="space-y-1.5">
							<Label>Arguments (JSON)</Label>
							<Textarea value={args} onChange={(e) => setArgs(e.target.value)} rows={8} className="font-mono text-xs" />
						</div>
						<Button onClick={run} disabled={running}>
							{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} Run tool
						</Button>
						<ResultView result={result} error={error} />
					</>
				)}
			</div>
		</div>
	);
}

function PromptsPanel({ prompts, client, connected }: { prompts: MCPPrompt[]; client: BrowserMCPClient | null; connected: boolean }) {
	const [selected, setSelected] = useState<MCPPrompt | null>(null);
	const [values, setValues] = useState<Record<string, string>>({});
	const [result, setResult] = useState<unknown>(undefined);
	const [error, setError] = useState<string | null>(null);
	const [running, setRunning] = useState(false);

	if (!connected) return <p className="text-muted-foreground text-sm">Connect to list prompts.</p>;
	return (
		<div className="grid gap-4 md:grid-cols-[320px_1fr]">
			<div className="max-h-[560px] space-y-1 overflow-auto rounded-md border p-1">
				{prompts.map((p) => (
					<button
						key={p.name}
						type="button"
						onClick={() => {
							setSelected(p);
							setValues({});
							setResult(undefined);
							setError(null);
						}}
						className={cn("hover:bg-muted w-full rounded px-2 py-1.5 text-left", selected?.name === p.name && "bg-muted")}
					>
						<div className="truncate font-mono text-xs">{p.name}</div>
						{p.description && <div className="text-muted-foreground line-clamp-1 text-xs">{p.description}</div>}
					</button>
				))}
				{prompts.length === 0 && <p className="text-muted-foreground p-2 text-sm">No prompts.</p>}
			</div>
			<div className="space-y-3">
				{!selected && <p className="text-muted-foreground text-sm">Pick a prompt.</p>}
				{selected && (
					<>
						<div className="font-mono text-sm font-medium">{selected.name}</div>
						<p className="text-muted-foreground text-sm">{selected.description}</p>
						{(selected.arguments ?? []).map((a) => (
							<div key={a.name} className="space-y-1">
								<Label>
									{a.name}
									{a.required && " *"}
								</Label>
								<Input
									value={values[a.name] ?? ""}
									onChange={(e) => setValues({ ...values, [a.name]: e.target.value })}
									placeholder={a.description}
								/>
							</div>
						))}
						<Button
							disabled={running}
							onClick={async () => {
								if (!client) return;
								setRunning(true);
								setError(null);
								try {
									setResult(await client.getPrompt(selected.name, values));
								} catch (e) {
									setError((e as Error).message);
								} finally {
									setRunning(false);
								}
							}}
						>
							{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} Get prompt
						</Button>
						{error && <pre className="border-destructive/50 bg-destructive/5 rounded-md border p-3 font-mono text-xs">{error}</pre>}
						{result !== undefined && (
							<pre className="bg-muted max-h-96 overflow-auto rounded-md p-3 font-mono text-xs whitespace-pre-wrap">{pretty(result)}</pre>
						)}
					</>
				)}
			</div>
		</div>
	);
}

function ResourcesPanel({
	resources,
	templates,
	client,
	connected,
}: {
	resources: MCPResource[];
	templates: MCPResourceTemplate[];
	client: BrowserMCPClient | null;
	connected: boolean;
}) {
	const [uri, setUri] = useState("");
	const [result, setResult] = useState<unknown>(undefined);
	const [error, setError] = useState<string | null>(null);
	const [running, setRunning] = useState(false);

	const read = async (value: string) => {
		if (!client || !value) return;
		setUri(value);
		setRunning(true);
		setError(null);
		try {
			setResult(await client.readResource(value));
		} catch (e) {
			setError((e as Error).message);
		} finally {
			setRunning(false);
		}
	};

	if (!connected) return <p className="text-muted-foreground text-sm">Connect to list resources.</p>;
	return (
		<div className="grid gap-4 md:grid-cols-[320px_1fr]">
			<div className="max-h-[560px] space-y-1 overflow-auto rounded-md border p-1">
				{resources.map((r) => (
					<button key={r.uri} type="button" onClick={() => read(r.uri)} className="hover:bg-muted w-full rounded px-2 py-1.5 text-left">
						<div className="truncate font-mono text-xs">{r.uri}</div>
						<div className="text-muted-foreground line-clamp-1 text-xs">{r.title || r.name}</div>
					</button>
				))}
				{templates.map((r) => (
					<button
						key={r.uriTemplate}
						type="button"
						onClick={() => setUri(r.uriTemplate)}
						className="hover:bg-muted w-full rounded px-2 py-1.5 text-left"
					>
						<div className="truncate font-mono text-xs">{r.uriTemplate}</div>
						<div className="text-muted-foreground text-xs">template · fill it in on the right</div>
					</button>
				))}
				{resources.length + templates.length === 0 && <p className="text-muted-foreground p-2 text-sm">No resources.</p>}
			</div>
			<div className="space-y-3">
				<div className="flex gap-2">
					<Input value={uri} onChange={(e) => setUri(e.target.value)} placeholder="resource URI" className="font-mono" />
					<Button onClick={() => read(uri)} disabled={!uri || running}>
						{running ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />} Read
					</Button>
				</div>
				{error && <pre className="border-destructive/50 bg-destructive/5 rounded-md border p-3 font-mono text-xs">{error}</pre>}
				{result !== undefined && (
					<pre className="bg-muted max-h-[560px] overflow-auto rounded-md p-3 font-mono text-xs whitespace-pre-wrap">{pretty(result)}</pre>
				)}
			</div>
		</div>
	);
}
