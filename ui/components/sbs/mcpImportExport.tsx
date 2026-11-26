// SBS fork: MetaMCP-style import / export of MCP servers as `mcpServers` JSON (the format Claude
// Desktop, Cursor and MetaMCP use). Import creates each server through the normal create API.
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation, useGetMCPClientsQuery } from "@/lib/store";
import type { CreateMCPClientRequest, MCPClient } from "@/lib/types/mcp";
import { Download, Loader2, Upload } from "lucide-react";
import { useState } from "react";

interface ImportedServer {
	type?: string;
	description?: string;
	command?: string;
	args?: string[];
	env?: Record<string, string>;
	url?: string;
	headers?: Record<string, string>;
	bearerToken?: string;
}

/** Bifrost MCP server names allow letters, digits and "_" only, and cannot start with a digit. */
export function toServerName(name: string): string {
	let out = name.replace(/[^A-Za-z0-9_]/g, "_");
	if (!out || /^[0-9]/.test(out)) out = `s_${out}`;
	return out;
}

/** Turns one `mcpServers` entry into a create request, or explains why it cannot. */
export function toCreateRequest(name: string, s: ImportedServer): CreateMCPClientRequest | string {
	const type = (s.type ?? (s.url ? "STREAMABLE_HTTP" : "STDIO")).toUpperCase().replace(/-/g, "_");
	const headers: Record<string, string> = { ...(s.headers ?? {}) };
	if (s.bearerToken) headers.Authorization = `Bearer ${s.bearerToken}`;
	const base: CreateMCPClientRequest = {
		name: toServerName(name),
		connection_type: "http",
		tools_to_execute: ["*"],
		is_ping_available: true,
		auth_type: Object.keys(headers).length ? "headers" : "none",
	};
	if (Object.keys(headers).length) {
		base.headers = Object.fromEntries(Object.entries(headers).map(([k, v]) => [k, { value: v }])) as CreateMCPClientRequest["headers"];
	}
	if (type === "STDIO") {
		if (!s.command) return "STDIO server without a command";
		return {
			...base,
			connection_type: "stdio",
			stdio_config: {
				command: s.command,
				args: s.args ?? [],
				// KEY=value; ${VAR} references are resolved from Bifrost's environment at start.
				envs: Object.entries(s.env ?? {}).map(([k, v]) => `${k}=${v}`),
			},
		};
	}
	if (!s.url) return `${type} server without a url`;
	return {
		...base,
		connection_type: type === "SSE" ? "sse" : "http",
		connection_string: { value: s.url } as CreateMCPClientRequest["connection_string"],
	};
}

/** The `mcpServers` JSON for the given servers. Secrets the API redacts stay redacted. */
export function toExportJSON(clients: MCPClient[]): string {
	const servers: Record<string, ImportedServer> = {};
	for (const c of clients) {
		const cfg = c.config;
		const entry: ImportedServer = {};
		if (cfg.connection_type === "stdio" && cfg.stdio_config) {
			entry.type = "STDIO";
			entry.command = cfg.stdio_config.command;
			if (cfg.stdio_config.args?.length) entry.args = cfg.stdio_config.args;
			const env: Record<string, string> = {};
			for (const e of cfg.stdio_config.envs ?? []) {
				const i = e.indexOf("=");
				if (i > 0) env[e.slice(0, i)] = e.slice(i + 1);
				else env[e] = `\${${e}}`;
			}
			if (Object.keys(env).length) entry.env = env;
		} else if (cfg.connection_type === "http" || cfg.connection_type === "sse") {
			entry.type = cfg.connection_type === "sse" ? "SSE" : "STREAMABLE_HTTP";
			const conn = cfg.connection_string;
			entry.url = conn?.ref ? `\${${conn.ref}}` : (conn?.value ?? "");
			if (cfg.headers && Object.keys(cfg.headers).length) {
				entry.headers = Object.fromEntries(Object.entries(cfg.headers).map(([k, v]) => [k, v?.ref ? `\${${v.ref}}` : (v?.value ?? "")]));
			}
		} else {
			continue; // OpenAPI-backed servers have no mcpServers equivalent
		}
		servers[cfg.name] = entry;
	}
	return JSON.stringify({ mcpServers: servers }, null, 2);
}

export function MCPImportExportButtons({ canCreate }: { canCreate: boolean }) {
	const { toast } = useToast();
	const [open, setOpen] = useState(false);
	const [text, setText] = useState("");
	const [running, setRunning] = useState(false);
	const [results, setResults] = useState<{ name: string; ok: boolean; message: string }[]>([]);
	const { data } = useGetMCPClientsQuery({ limit: 100, offset: 0 });
	const [createMCPClient] = useCreateMCPClientMutation();

	const exportJSON = () => {
		const json = toExportJSON(data?.clients ?? []);
		const blob = new Blob([json], { type: "application/json" });
		const url = URL.createObjectURL(blob);
		const a = document.createElement("a");
		a.href = url;
		a.download = "mcp-servers.json";
		a.click();
		URL.revokeObjectURL(url);
	};

	const runImport = async () => {
		let parsed: { mcpServers?: Record<string, ImportedServer> } | Record<string, ImportedServer>;
		try {
			parsed = JSON.parse(text);
		} catch (e) {
			toast({ title: "Not valid JSON", description: (e as Error).message, variant: "destructive" });
			return;
		}
		const servers = ((parsed as { mcpServers?: Record<string, ImportedServer> }).mcpServers ?? parsed) as Record<string, ImportedServer>;
		const existing = new Set((data?.clients ?? []).map((c) => c.config.name));
		setRunning(true);
		const out: { name: string; ok: boolean; message: string }[] = [];
		for (const [name, server] of Object.entries(servers)) {
			const req = toCreateRequest(name, server);
			if (typeof req === "string") {
				out.push({ name, ok: false, message: req });
				continue;
			}
			if (existing.has(req.name)) {
				out.push({ name, ok: false, message: `already exists as ${req.name}` });
				continue;
			}
			try {
				await createMCPClient(req).unwrap();
				out.push({ name, ok: true, message: req.name !== name ? `imported as ${req.name}` : "imported" });
			} catch (err) {
				out.push({ name, ok: false, message: getErrorMessage(err) });
			}
		}
		setResults(out);
		setRunning(false);
		const ok = out.filter((r) => r.ok).length;
		toast({ title: `Imported ${ok} of ${out.length} servers`, variant: ok === out.length ? "default" : "destructive" });
	};

	return (
		<>
			<Button variant="outline" className="h-8" onClick={exportJSON} data-testid="mcp-export-btn">
				<Download /> <span className="hidden sm:inline">Export</span>
			</Button>
			<Button variant="outline" className="h-8" onClick={() => setOpen(true)} disabled={!canCreate} data-testid="mcp-import-btn">
				<Upload /> <span className="hidden sm:inline">Import</span>
			</Button>
			<Dialog
				open={open}
				onOpenChange={(v) => {
					setOpen(v);
					if (!v) setResults([]);
				}}
			>
				<DialogContent className="max-w-2xl">
					<DialogHeader>
						<DialogTitle>Import MCP servers</DialogTitle>
						<DialogDescription>
							Paste an <code>mcpServers</code> JSON from Claude Desktop, Cursor or a MetaMCP export. Env values may reference Bifrost&apos;s
							environment as <code>{"${VAR}"}</code>. Names become valid server names (hyphens turn into underscores).
						</DialogDescription>
					</DialogHeader>
					<Textarea
						value={text}
						onChange={(e) => setText(e.target.value)}
						rows={12}
						className="font-mono text-xs"
						placeholder={
							'{\n  "mcpServers": {\n    "grafana": { "command": "uvx", "args": ["mcp-grafana"], "env": { "GRAFANA_URL": "${GRAFANA_URL}" } },\n    "kali": { "type": "STREAMABLE_HTTP", "url": "http://kali-mcp:8000/mcp" }\n  }\n}'
						}
						data-testid="mcp-import-json"
					/>
					{results.length > 0 && (
						<ul className="max-h-48 space-y-1 overflow-auto text-sm">
							{results.map((r) => (
								<li key={r.name} className={r.ok ? "" : "text-destructive"}>
									<span className="font-mono">{r.name}</span>: {r.message}
								</li>
							))}
						</ul>
					)}
					<DialogFooter>
						<Button variant="outline" onClick={() => setOpen(false)}>
							Close
						</Button>
						<Button onClick={runImport} disabled={!text.trim() || running} data-testid="mcp-import-run-btn">
							{running && <Loader2 className="h-4 w-4 animate-spin" />} Import
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</>
	);
}
