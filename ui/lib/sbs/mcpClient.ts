// SBS fork: a minimal Streamable HTTP MCP client for the browser, used by the MCP Inspector.
// It talks to this Bifrost's own /mcp/<slug> (same origin), with a virtual key.
import { withBasePath } from "@/lib/utils/basePath";

export interface MCPTool {
	name: string;
	title?: string;
	description?: string;
	inputSchema?: { type?: string; properties?: Record<string, JSONSchema>; required?: string[] } & Record<string, unknown>;
	annotations?: Record<string, unknown>;
}

export interface JSONSchema {
	type?: string | string[];
	description?: string;
	enum?: unknown[];
	default?: unknown;
	items?: JSONSchema;
	properties?: Record<string, JSONSchema>;
	[key: string]: unknown;
}

export interface MCPPrompt {
	name: string;
	title?: string;
	description?: string;
	arguments?: { name: string; description?: string; required?: boolean }[];
}

export interface MCPResource {
	uri: string;
	name?: string;
	title?: string;
	description?: string;
	mimeType?: string;
}

export interface MCPResourceTemplate {
	uriTemplate: string;
	name?: string;
	description?: string;
	mimeType?: string;
}

export interface MCPTraffic {
	at: number;
	direction: "request" | "response" | "error";
	method: string;
	payload: unknown;
	ms?: number;
}

export class MCPError extends Error {
	constructor(
		message: string,
		public status?: number,
		public code?: number,
	) {
		super(message);
	}
}

export function mcpEndpointPath(slug: string): string {
	return withBasePath(`/mcp/${encodeURIComponent(slug)}`);
}

export function mcpEndpointUrl(slug: string): string {
	return `${window.location.origin}${mcpEndpointPath(slug)}`;
}

/** Reads the JSON-RPC message answering id from an SSE body. */
async function readSSE(response: Response, id: number): Promise<unknown> {
	const text = await response.text();
	for (const block of text.split(/\r?\n\r?\n/)) {
		const data = block
			.split(/\r?\n/)
			.filter((line) => line.startsWith("data:"))
			.map((line) => line.slice(5).replace(/^ /, ""))
			.join("\n");
		if (!data) continue;
		try {
			const msg = JSON.parse(data);
			if (msg && msg.id === id) return msg;
		} catch {
			// not JSON: skip
		}
	}
	throw new MCPError("MCP stream ended without a response");
}

export class BrowserMCPClient {
	private sessionId?: string;
	private nextId = 0;
	public serverInfo?: { name?: string; version?: string };
	public capabilities?: Record<string, unknown>;
	public instructions?: string;

	constructor(
		private slug: string,
		private virtualKey: string,
		private onTraffic?: (entry: MCPTraffic) => void,
	) {}

	private async post(method: string, params: unknown, notify = false): Promise<unknown> {
		const body: Record<string, unknown> = { jsonrpc: "2.0", method };
		if (params !== undefined) body.params = params;
		const id = notify ? 0 : ++this.nextId;
		if (!notify) body.id = id;
		const headers: Record<string, string> = {
			"Content-Type": "application/json",
			Accept: "application/json, text/event-stream",
			"MCP-Protocol-Version": "2025-06-18",
		};
		if (this.virtualKey) headers["x-bf-vk"] = this.virtualKey;
		if (this.sessionId) headers["Mcp-Session-Id"] = this.sessionId;

		const started = performance.now();
		if (!notify) this.onTraffic?.({ at: Date.now(), direction: "request", method, payload: body });
		const response = await fetch(mcpEndpointPath(this.slug), { method: "POST", headers, body: JSON.stringify(body), credentials: "omit" });
		const sid = response.headers.get("Mcp-Session-Id");
		if (sid) this.sessionId = sid;
		if (!response.ok) {
			let message = `${response.status} ${response.statusText}`;
			try {
				const text = await response.text();
				try {
					const parsed = JSON.parse(text);
					message = parsed?.error?.message ?? parsed?.message ?? text ?? message;
				} catch {
					if (text) message = text;
				}
			} catch {
				// keep the status line
			}
			this.onTraffic?.({ at: Date.now(), direction: "error", method, payload: message, ms: performance.now() - started });
			throw new MCPError(message, response.status);
		}
		if (notify) return undefined;
		const msg = (response.headers.get("Content-Type") ?? "").startsWith("text/event-stream")
			? await readSSE(response, id)
			: await response.json();
		const ms = performance.now() - started;
		const envelope = msg as { result?: unknown; error?: { code: number; message: string } };
		if (envelope.error) {
			this.onTraffic?.({ at: Date.now(), direction: "error", method, payload: envelope.error, ms });
			throw new MCPError(envelope.error.message, undefined, envelope.error.code);
		}
		this.onTraffic?.({ at: Date.now(), direction: "response", method, payload: envelope.result, ms });
		return envelope.result;
	}

	async connect(): Promise<void> {
		const result = (await this.post("initialize", {
			protocolVersion: "2025-06-18",
			capabilities: {},
			clientInfo: { name: "bifrost-sbs-inspector", version: "1.0.0" },
		})) as { serverInfo?: { name?: string; version?: string }; capabilities?: Record<string, unknown>; instructions?: string };
		this.serverInfo = result?.serverInfo;
		this.capabilities = result?.capabilities;
		this.instructions = result?.instructions;
		await this.post("notifications/initialized", undefined, true);
	}

	private async paged<T>(method: string, key: string): Promise<T[]> {
		const all: T[] = [];
		let cursor: string | undefined;
		for (let page = 0; page < 50; page++) {
			const result = (await this.post(method, cursor ? { cursor } : undefined)) as Record<string, unknown> & { nextCursor?: string };
			all.push(...(((result?.[key] as T[]) ?? []) as T[]));
			if (!result?.nextCursor) break;
			cursor = result.nextCursor;
		}
		return all;
	}

	listTools(): Promise<MCPTool[]> {
		return this.paged<MCPTool>("tools/list", "tools");
	}
	listPrompts(): Promise<MCPPrompt[]> {
		return this.paged<MCPPrompt>("prompts/list", "prompts");
	}
	listResources(): Promise<MCPResource[]> {
		return this.paged<MCPResource>("resources/list", "resources");
	}
	listResourceTemplates(): Promise<MCPResourceTemplate[]> {
		return this.paged<MCPResourceTemplate>("resources/templates/list", "resourceTemplates");
	}
	callTool(name: string, args: Record<string, unknown>): Promise<unknown> {
		return this.post("tools/call", { name, arguments: args });
	}
	getPrompt(name: string, args: Record<string, string>): Promise<unknown> {
		return this.post("prompts/get", { name, arguments: args });
	}
	readResource(uri: string): Promise<unknown> {
		return this.post("resources/read", { uri });
	}
	ping(): Promise<unknown> {
		return this.post("ping", undefined);
	}
	async close(): Promise<void> {
		if (!this.sessionId) return;
		const headers: Record<string, string> = { "Mcp-Session-Id": this.sessionId };
		if (this.virtualKey) headers["x-bf-vk"] = this.virtualKey;
		try {
			await fetch(mcpEndpointPath(this.slug), { method: "DELETE", headers, credentials: "omit" });
		} catch {
			// best effort
		}
		this.sessionId = undefined;
	}
}

/** A starting argument object for a tool, from its input schema's defaults and types. */
export function exampleArgs(schema?: MCPTool["inputSchema"]): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const [key, prop] of Object.entries(schema?.properties ?? {})) {
		if (prop.default !== undefined) out[key] = prop.default;
		else if (prop.enum?.length) out[key] = prop.enum[0];
		else {
			const type = Array.isArray(prop.type) ? prop.type[0] : prop.type;
			if (!schema?.required?.includes(key)) continue;
			out[key] =
				type === "number" || type === "integer" ? 0 : type === "boolean" ? false : type === "array" ? [] : type === "object" ? {} : "";
		}
	}
	return out;
}
