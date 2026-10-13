import type {
	MCPOpenAPIConfig,
	MCPOpenAPICredential,
	MCPOpenAPIPreviewResponse,
	MCPOpenAPIPreviewTool,
	MCPOpenAPISecurityScheme,
	MCPOpenAPIUnsupportedOperation,
	SecretVar,
} from "@/lib/types/mcp";

/** How the user supplied the document in the create/replace sheet. */
export type OpenAPISourceMode = "paste" | "url" | "upload";

/** Client-side cap on a pasted or uploaded document, mirroring the server's 5 MiB limit. */
export const OPENAPI_MAX_SPEC_BYTES = 5 * 1024 * 1024;

/** Picks the editor language from the document's first non-blank character. */
export function detectSpecLanguage(text: string | undefined): "json" | "yaml" {
	const trimmed = (text ?? "").replace(/^﻿/, "").trimStart();
	if (trimmed.startsWith("{") || trimmed.startsWith("[")) return "json";
	return "yaml";
}

/**
 * Identity of the source the preview was computed from, so a later edit to the
 * text or URL invalidates the preview until the user parses again. The client
 * name is part of it because long tool names are shortened to fit beside it.
 */
export function specFingerprint(
	mode: OpenAPISourceMode,
	spec: string | undefined,
	specUrl: string | undefined,
	clientName?: string,
): string {
	// Encoded as a JSON array so no part can run into the next one.
	const source = mode === "url" ? (specUrl ?? "").trim() : (spec ?? "");
	return JSON.stringify([mode, source, (clientName ?? "").trim()]);
}

/** One row of the operation picker: supported tools and unsupported operations, in one list. */
export interface OpenAPIPreviewRow {
	key: string;
	name: string;
	operationId?: string;
	method: string;
	path: string;
	description?: string;
	deprecated: boolean;
	supported: boolean;
	skipReason?: string;
	warnings: string[];
}

export function mergePreviewRows(
	tools: MCPOpenAPIPreviewTool[] | undefined,
	unsupported: MCPOpenAPIUnsupportedOperation[] | undefined,
): OpenAPIPreviewRow[] {
	const rows: OpenAPIPreviewRow[] = (tools ?? []).map((tool) => ({
		key: tool.name,
		name: tool.name,
		operationId: tool.operation_id,
		method: tool.method,
		path: tool.path,
		description: tool.summary || tool.description,
		deprecated: !!tool.deprecated,
		supported: true,
		warnings: tool.warnings ?? [],
	}));
	for (const op of unsupported ?? []) {
		rows.push({
			key: `${op.method} ${op.path}`,
			name: op.operation_id || `${op.method.toLowerCase()} ${op.path}`,
			operationId: op.operation_id,
			method: op.method,
			path: op.path,
			deprecated: /deprecated/i.test(op.reason),
			supported: false,
			skipReason: op.reason,
			warnings: [],
		});
	}
	return rows;
}

export function supportedToolNames(preview: MCPOpenAPIPreviewResponse | null | undefined): string[] {
	return (preview?.tools ?? []).map((tool) => tool.name);
}

/**
 * The wire form of a tool selection: `["*"]` when every supported tool is
 * picked, otherwise the picked names in preview order (unknown names dropped).
 */
export function collapseToolSelection(selected: string[], supported: string[]): string[] {
	const picked = new Set(selected);
	const kept = supported.filter((name) => picked.has(name));
	if (supported.length > 0 && kept.length === supported.length) return ["*"];
	return kept;
}

/** Expands a stored allow-list back into explicit names for the picker. */
export function expandToolSelection(stored: string[] | undefined, supported: string[]): string[] {
	if (!stored) return [];
	if (stored.includes("*")) return [...supported];
	const known = new Set(supported);
	return stored.filter((name) => known.has(name));
}

/**
 * Carries a selection across a re-parse: an "everything" selection stays
 * everything (new tools included); a partial selection keeps the tools that
 * still exist, and leaves newly appeared tools unselected. When both previews'
 * tools are given, a tool is followed by its method and path, so an operation
 * whose shortened name changed (the server was renamed) stays selected.
 */
export function reconcileToolSelection(
	previous: string[],
	previousAll: string[],
	nextSupported: string[],
	previousTools?: MCPOpenAPIPreviewTool[],
	nextTools?: MCPOpenAPIPreviewTool[],
): string[] {
	const wasAll = previous.includes("*") || (previousAll.length > 0 && previousAll.every((name) => previous.includes(name)));
	if (wasAll) return [...nextSupported];
	const selected = new Set(previous);
	let kept = selected;
	if (previousTools && nextTools) {
		// The selection is rebuilt from operations alone: a name is not carried
		// over by itself, since an operation that was never selected can inherit
		// it (dedupe suffixes shift when another operation disappears).
		const operationKey = (t: MCPOpenAPIPreviewTool) => `${t.method.toUpperCase()} ${t.path}`;
		const nextByOperation = new Map(nextTools.map((t) => [operationKey(t), t.name]));
		kept = new Set<string>();
		for (const tool of previousTools) {
			const next = selected.has(tool.name) ? nextByOperation.get(operationKey(tool)) : undefined;
			if (next) kept.add(next);
		}
	}
	return nextSupported.filter((name) => kept.has(name));
}

export type OpenAPICredentialField = "value" | "username" | "password";

export interface OpenAPISecurityInput {
	scheme: MCPOpenAPISecurityScheme;
	label: string;
	fields: { field: OpenAPICredentialField; label: string; placeholder: string }[];
	helper?: string;
}

/** Renders a security scheme as the credential inputs it needs. Unsupported schemes get no fields. */
export function securitySchemeToInputs(scheme: MCPOpenAPISecurityScheme): OpenAPISecurityInput {
	if (!scheme.supported) {
		return { scheme, label: scheme.name, fields: [], helper: scheme.reason };
	}
	if (scheme.type === "apiKey") {
		const where = scheme.in ?? "header";
		return {
			scheme,
			label: `${scheme.name} · ${scheme.param_name ?? "API key"} (${where})`,
			fields: [{ field: "value", label: "API key", placeholder: `Value or env.${toEnvName(scheme.name)}` }],
			helper: scheme.description,
		};
	}
	if (scheme.type === "http" && scheme.scheme === "basic") {
		return {
			scheme,
			label: `${scheme.name} · Basic authentication`,
			fields: [
				{ field: "username", label: "Username", placeholder: "Username" },
				{ field: "password", label: "Password", placeholder: `Password or env.${toEnvName(scheme.name)}_PASSWORD` },
			],
			helper: scheme.description,
		};
	}
	return {
		scheme,
		label: `${scheme.name} · Bearer token`,
		fields: [{ field: "value", label: "Bearer token", placeholder: `Token or env.${toEnvName(scheme.name)}` }],
		helper: scheme.description,
	};
}

function toEnvName(name: string): string {
	return (
		name
			.replace(/[^A-Za-z0-9]+/g, "_")
			.replace(/^_+|_+$/g, "")
			.toUpperCase() || "API_KEY"
	);
}

function isSecretSet(v: SecretVar | undefined): boolean {
	return !!v && (!!v.value?.trim() || !!v.ref?.trim());
}

/** Drops credentials with no value set, so an untouched scheme is not sent as an empty secret. */
export function pruneCredentials(
	creds: Record<string, MCPOpenAPICredential> | undefined,
	schemes: MCPOpenAPISecurityScheme[] | undefined,
): Record<string, MCPOpenAPICredential> | undefined {
	if (!creds) return undefined;
	const declared = schemes ? new Set(schemes.filter((s) => s.supported).map((s) => s.name)) : undefined;
	// No prototype: a scheme may be named "__proto__", which a plain object would swallow.
	const out: Record<string, MCPOpenAPICredential> = Object.create(null);
	const basic = new Set((schemes ?? []).filter((s) => s.type === "http" && s.scheme === "basic").map((s) => s.name));
	for (const [name, cred] of Object.entries(creds)) {
		if (declared && !declared.has(name)) continue;
		// The server keeps a stored secret for a field left empty, so a Basic
		// credential with one field cleared is dropped whole; sending only the
		// other field would leave the old secret in place.
		if (basic.has(name) && !(isSecretSet(cred.username) && isSecretSet(cred.password))) continue;
		const kept: MCPOpenAPICredential = {};
		if (isSecretSet(cred.value)) kept.value = cred.value;
		if (isSecretSet(cred.username)) kept.username = cred.username;
		if (isSecretSet(cred.password)) kept.password = cred.password;
		if (Object.keys(kept).length > 0) out[name] = kept;
	}
	return Object.keys(out).length > 0 ? out : undefined;
}

/**
 * The openapi_config sent on create or replace: the document inline (URL mode
 * stores the text fetched for the preview, never older pasted or stored text,
 * and keeps the URL as provenance; paste and upload clear a stored URL), the
 * base URL as shown (empty clears a stored override), and the credentials
 * that were filled in. A provided credential map whose values were all cleared
 * is sent as {} so the server clears the stored secrets; leaving
 * security_credentials undefined keeps them.
 */
export function buildOpenAPIConfigPayload(
	cfg: MCPOpenAPIConfig | undefined,
	mode: OpenAPISourceMode,
	preview: MCPOpenAPIPreviewResponse | null | undefined,
): MCPOpenAPIConfig {
	const spec = mode === "url" ? preview?.spec : cfg?.spec?.trim() ? cfg.spec : preview?.spec;
	// The fetched text is saved with the URL it was read from after redirects,
	// so its relative servers resolve where the preview showed them.
	const specUrl = (mode === "url" ? preview?.spec_url?.trim() : undefined) || cfg?.spec_url?.trim();
	const out: MCPOpenAPIConfig = {};
	if (spec) out.spec = spec;
	if (mode === "url") {
		if (specUrl) out.spec_url = specUrl;
	} else {
		out.spec_url = "";
	}
	out.base_url = cfg?.base_url?.trim() ?? "";
	if (cfg?.security_credentials !== undefined) {
		out.security_credentials = pruneCredentials(cfg.security_credentials, preview?.security_schemes) ?? {};
	}
	if (cfg?.include_deprecated) out.include_deprecated = true;
	if (cfg?.max_response_bytes) out.max_response_bytes = cfg.max_response_bytes;
	return out;
}

export function formatBytes(n: number | undefined): string {
	if (!n || n <= 0) return "0 B";
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
	return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function isSpecTooLarge(bytes: number, max: number = OPENAPI_MAX_SPEC_BYTES): boolean {
	return bytes > max;
}

/** One-line description of a stored openapi_config for the edit sheet. */
export function summarizeSpecSource(cfg: MCPOpenAPIConfig | undefined): string {
	if (!cfg) return "-";
	const parts: string[] = [];
	const title = cfg.spec_title
		? `${cfg.spec_title}${cfg.openapi_version ? ` (OpenAPI ${cfg.openapi_version})` : ""}`
		: cfg.openapi_version
			? `OpenAPI ${cfg.openapi_version}`
			: "";
	if (title) parts.push(title);
	parts.push(cfg.spec_url ? cfg.spec_url : "inline spec");
	if (cfg.spec_size) parts.push(formatBytes(cfg.spec_size));
	if (cfg.operation_count !== undefined) parts.push(`${cfg.operation_count} operation${cfg.operation_count === 1 ? "" : "s"}`);
	return parts.join(" · ");
}