import type {
	MCPOpenAPIConfig,
	MCPOpenAPICredential,
	MCPOpenAPIPreviewRequest,
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
				{
					field: "password",
					label: "Password",
					placeholder: `Password or env.${toEnvName(scheme.name)}_PASSWORD`,
				},
			],
			helper: scheme.description,
		};
	}
	return {
		scheme,
		label: `${scheme.name} · Bearer token`,
		fields: [
			{
				field: "value",
				label: "Bearer token",
				placeholder: `Token or env.${toEnvName(scheme.name)}`,
			},
		],
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
	baseUrlChoice?: "server" | "custom",
	perUserSchemes: string[] = [],
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
	// The document's default server (its first absolute one) is sent as ""
	// rather than pinned: the backend resolves the same default, operation-level
	// servers keep applying, and on update "" clears an earlier override. The
	// preview's base_url is not the default: a re-parse echoes a chosen server.
	const base = cfg?.base_url?.trim() ?? "";
	const documentDefault = preview?.servers?.find((srv) => /^https?:\/\/[^/]/i.test(srv));
	// A custom URL is an explicit override even when it equals the default:
	// it also wins over operation-level servers, which "" would not.
	const isDocumentDefault = baseUrlChoice !== "custom" && !!documentDefault && trimSlash(documentDefault) === trimSlash(base);
	out.base_url = base && !isDocumentDefault ? base : "";
	if (cfg?.security_credentials !== undefined) {
		const pruned = pruneCredentials(cfg.security_credentials, preview?.security_schemes) ?? {};
		// A scheme each caller supplies through a per-user header has no stored value.
		for (const name of perUserSchemes) delete pruned[name];
		out.security_credentials = pruned;
	}
	if (cfg?.include_deprecated) out.include_deprecated = true;
	if (cfg?.max_response_bytes) out.max_response_bytes = cfg.max_response_bytes;
	return out;
}

// matchServer returns the listed document server equal to url, ignoring a
// trailing slash.
function matchServer(preview: MCPOpenAPIPreviewResponse, url: string): string | undefined {
	return preview.servers?.find((srv) => trimSlash(srv) === trimSlash(url));
}

function trimSlash(url: string): string {
	return url.replace(/\/+$/, "");
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
/** The form values a parse response is applied to. */
export interface OpenAPIParseState {
	selection: string[];
	baseUrl: string;
	baseUrlChoice: "server" | "custom";
	credentials: Record<string, MCPOpenAPICredential>;
}

/**
 * Applies a parse (preview) response to the form: the tool selection is
 * reconciled against the previous preview, the base URL follows the document
 * unless the user typed a custom one or picked a server the document still
 * lists, and credentials for schemes the document no longer declares are
 * dropped. current must be read when the response arrives, not when the parse
 * started, so edits made while it was in flight survive.
 */
export function applyParsedPreview(
	current: OpenAPIParseState,
	previousPreview: MCPOpenAPIPreviewResponse | null | undefined,
	response: MCPOpenAPIPreviewResponse,
): OpenAPIParseState {
	const nextSupported = supportedToolNames(response);
	const selection = previousPreview
		? reconcileToolSelection(current.selection, supportedToolNames(previousPreview), nextSupported, previousPreview.tools, response.tools)
		: nextSupported;
	let { baseUrl, baseUrlChoice } = current;
	// With no document servers to pick from, a typed URL is an override even if
	// the choice was never switched to custom. A server picked from the previous
	// document is not typed: when the new document drops it, it is cleared.
	const pickedFromPrevious = current.baseUrlChoice === "server" && !!previousPreview && !!matchServer(previousPreview, current.baseUrl);
	const typedWithoutServers = selectableServers(response).length === 0 && !!current.baseUrl.trim() && !pickedFromPrevious;
	const keepCustom = (current.baseUrlChoice === "custom" || typedWithoutServers) && !!current.baseUrl.trim();
	if (typedWithoutServers) baseUrlChoice = "custom";
	// Servers are matched without a trailing slash (the resolved base_url drops
	// it, the listed server may keep it) and stored as the listed string, so the
	// picker shows it selected.
	const pickedServer = current.baseUrlChoice === "server" && current.baseUrl ? matchServer(response, current.baseUrl) : undefined;
	const keepPicked = !!pickedServer;
	if (pickedServer) baseUrl = pickedServer;
	const defaultServer = response.base_url ? matchServer(response, response.base_url) : undefined;
	if (!keepCustom && !keepPicked) {
		if (response.operation_servers && !response.base_url) {
			// Every operation carries its own server: no base URL, nothing to pick.
			baseUrlChoice = "server";
			baseUrl = "";
		} else if (defaultServer) {
			baseUrlChoice = "server";
			baseUrl = defaultServer;
		} else {
			baseUrlChoice = "custom";
			baseUrl = response.base_url ?? "";
		}
	}
	// A credential carries over only to a scheme of the same name that still
	// takes it the same way: a document that reuses the name for another type
	// or location must not receive the old secret.
	const schemeShape = (scheme: MCPOpenAPISecurityScheme) =>
		[scheme.type, scheme.in ?? "", scheme.param_name ?? "", scheme.scheme ?? ""].join("|");
	const previousShapes = new Map((previousPreview?.security_schemes ?? []).map((scheme) => [scheme.name, schemeShape(scheme)]));
	const declared = new Map(
		(response.security_schemes ?? []).filter((scheme) => scheme.supported).map((scheme) => [scheme.name, schemeShape(scheme)]),
	);
	// No prototype, so a scheme named __proto__ is stored as an entry.
	const credentials: Record<string, MCPOpenAPICredential> = Object.create(null);
	for (const [name, cred] of Object.entries(current.credentials)) {
		const shape = declared.get(name);
		if (shape === undefined) continue;
		if (previousPreview && previousShapes.get(name) !== shape) continue;
		credentials[name] = cred;
	}
	return { selection, baseUrl, baseUrlChoice, credentials };
}

/** Reads an uploaded spec file, returning its text or the error to show. */
export async function readUploadedSpec(file: {
	size: number;
	text: () => Promise<string>;
}): Promise<{ text: string; error: string | null }> {
	if (isSpecTooLarge(file.size)) {
		return {
			text: "",
			error: `The file exceeds the ${formatBytes(OPENAPI_MAX_SPEC_BYTES)} limit for OpenAPI documents.`,
		};
	}
	let text: string;
	try {
		text = await file.text();
	} catch (error) {
		return {
			text: "",
			error: `The file could not be read: ${error instanceof Error ? error.message : String(error)}`,
		};
	}
	return { text, error: text.trim() ? null : "The file is empty." };
}
/**
 * The header a security scheme travels in, suggested as the per-user header
 * that carries it: an apiKey header's own name, Authorization for bearer and
 * basic. Query, cookie and unsupported schemes cannot come from a header.
 */
export function defaultSchemeHeader(scheme: MCPOpenAPISecurityScheme): string | null {
	if (!scheme.supported) return null;
	if (scheme.type === "apiKey") return scheme.in === "header" && scheme.param_name ? scheme.param_name : null;
	if (scheme.type === "http") return "Authorization";
	return null;
}

/**
 * The per-user header each header-carried scheme is taken from: the name the
 * admin typed, else the suggested one. A cleared name opts the scheme out.
 */
export function perUserSchemeHeaders(
	preview: MCPOpenAPIPreviewResponse | null | undefined,
	typed: Record<string, string>,
): Record<string, string> {
	const out: Record<string, string> = Object.create(null);
	for (const scheme of preview?.security_schemes ?? []) {
		const suggested = defaultSchemeHeader(scheme);
		if (suggested === null) continue;
		const name = (Object.hasOwn(typed, scheme.name) ? typed[scheme.name] : suggested).trim();
		if (name) out[scheme.name] = name;
	}
	return out;
}

/** Required header keys plus the scheme headers, without case-insensitive duplicates. */
export function mergePerUserHeaderKeys(keys: string[], schemeHeaders: string[]): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const key of [...keys, ...schemeHeaders]) {
		const lower = key.toLowerCase();
		if (seen.has(lower)) continue;
		seen.add(lower);
		out.push(key);
	}
	return out;
}

/**
 * Whether Create first runs the per-user headers sample-values check. An
 * OpenAPI server has no MCP server to verify against (the backend skips the
 * check and ignores the samples), so it is created directly with the declared
 * header names.
 */
export function needsHeadersVerification(payload: { auth_type?: string; connection_type?: string }): boolean {
	return payload.auth_type === "per_user_headers" && payload.connection_type !== "openapi";
}

/** Delay after the last edit before a pasted or uploaded document is parsed. */
export const OPENAPI_AUTO_PARSE_DELAY_MS = 600;

/**
 * How the spec source offers parsing. A pasted or uploaded document is parsed
 * on its own once the text settles, so the button only comes back to retry a
 * failed parse. A URL is fetched only on request, so a half-typed address is
 * never fetched; its button stays until the fetched document is current.
 */
export function openAPIParseControls(state: {
	mode: OpenAPISourceMode;
	isCurrent: boolean;
	hasError: boolean;
	canParse: boolean;
	isParsing: boolean;
}): { autoParse: boolean; showButton: boolean; showPending: boolean; buttonLabel: string } {
	if (state.mode === "url") {
		return { autoParse: false, showButton: !state.isCurrent || state.hasError, showPending: false, buttonLabel: "Fetch spec" };
	}
	return {
		autoParse: true,
		showButton: state.hasError && !state.isParsing,
		showPending: state.isParsing || (state.canParse && !state.isCurrent && !state.hasError),
		buttonLabel: "Parse again",
	};
}

/**
 * Whether a settled pasted or uploaded source is auto-parsed now, and which
 * source is remembered as already attempted. A source is attempted once, so a
 * failed parse waits for an edit or the retry button instead of firing again;
 * the attempt is forgotten once another source is selected, since coming back
 * to it means the preview on screen belongs to that other source.
 */
export function autoParseStep(
	remembered: string | null,
	sourceKey: string,
	shouldAutoParse: boolean,
): { parse: boolean; remembered: string | null } {
	if (remembered !== sourceKey) remembered = null;
	if (!shouldAutoParse || remembered === sourceKey) return { parse: false, remembered };
	return { parse: true, remembered: sourceKey };
}

/** A rejected upload's message, tied to the file name shown when it happened. */
export interface UploadErrorState {
	message: string;
	fileName: string;
}

/**
 * Applies a read upload. A file that was read replaces the document; a
 * rejected one leaves the current document and file name in place and only
 * reports why.
 */
export function applyUploadedSpec(
	currentFileName: string,
	result: { text: string; error: string | null },
): { accept: boolean; error: UploadErrorState | null } {
	if (result.error) return { accept: false, error: { message: result.error, fileName: currentFileName } };
	return { accept: true, error: null };
}

/** A rejection is shown until the shown file changes (another file picked, or the form reset). */
export function visibleUploadError(error: UploadErrorState | null, fileName: string): string | null {
	return error && error.fileName === fileName ? error.message : null;
}

/**
 * Whether the form must have a base URL before the server can be created or
 * replaced. A document with no top-level server needs one unless every
 * operation declares its own (then a base URL is only an optional override).
 * Otherwise a custom choice needs its value.
 */
export function openAPIBaseUrlRequired(choice: "server" | "custom", preview: MCPOpenAPIPreviewResponse | null | undefined): boolean {
	if (selectableServers(preview).length === 0) return !preview?.operation_servers;
	return choice === "custom";
}
/**
 * The document servers that can be picked as the upstream: absolute http(s)
 * URLs only. Relative entries the parser could not resolve stay in the preview
 * for display, but cannot serve as a base URL.
 */
export function selectableServers(preview: MCPOpenAPIPreviewResponse | null | undefined): string[] {
	return (preview?.servers ?? []).filter((srv) => /^https?:\/\/[^/]/i.test(srv));
}

/**
 * Whether the edit sheet's spec replacement holds changes worth saving: an open
 * replacement with a parsed preview, edited credentials, or a source (text, file
 * or URL) changed from what was loaded, even before it is parsed. It lives
 * outside the form, so the sheet adds it to the form's own dirty state.
 */
export function replacementIsDirty(r: {
	open: boolean;
	preview: MCPOpenAPIPreviewResponse | null;
	credentialsDirty: boolean;
	sourceEdited?: boolean;
}): boolean {
	return r.open && (!!r.preview || r.credentialsDirty || !!r.sourceEdited);
}

/**
 * Follows saved tool names from the stored document's operations to a
 * replacement's by method and path, since a dedupe suffix can move to another
 * operation when one disappears. A name whose operation is gone maps to
 * undefined, and so does every name when the stored operations are unknown,
 * since keeping it could hand it to another operation. Without a replacement's
 * tools there is nothing to follow, and names are kept as they are.
 */
function followToolName(
	name: string,
	storedTools: MCPOpenAPIPreviewTool[] | undefined,
	nextTools: MCPOpenAPIPreviewTool[] | undefined,
): string | undefined {
	if (!nextTools) return name;
	if (!storedTools) return undefined;
	const operationKey = (t: MCPOpenAPIPreviewTool) => `${t.method.toUpperCase()} ${t.path}`;
	const stored = storedTools.find((t) => t.name === name);
	if (!stored) return undefined;
	return nextTools.find((t) => operationKey(t) === operationKey(stored))?.name;
}

/**
 * The auto-execute list to save with a replaced document's tool selection: an
 * entry survives only if its operation is still offered and selected, under
 * the name the replacement gives it (followed by method and path when both
 * documents' tools are known, so a renamed tool never inherits another's
 * approval). "*" stays "*" while every tool is selected, and otherwise becomes
 * the selected tools, since the server refuses "*" next to a partial
 * tools_to_execute.
 */
export function replacementAutoExecute(
	autoExecute: string[] | undefined,
	supported: string[],
	toolsToExecute: string[],
	storedTools?: MCPOpenAPIPreviewTool[],
	nextTools?: MCPOpenAPIPreviewTool[],
): string[] {
	const all = toolsToExecute.includes("*");
	if (!all && autoExecute?.includes("*")) return toolsToExecute.filter((name) => supported.includes(name));
	const out: string[] = [];
	for (const entry of autoExecute ?? []) {
		if (entry === "*") {
			if (all) out.push(entry);
			continue;
		}
		const name = followToolName(entry, storedTools, nextTools);
		if (name && supported.includes(name) && (all || toolsToExecute.includes(name)) && !out.includes(name)) out.push(name);
	}
	return out;
}

/**
 * The tool prices to save with a replaced document: each price follows its
 * operation to the name the replacement gives it, and the price of an
 * operation the replacement drops is removed (as is every price when the
 * stored document's operations are unknown).
 */
export function replacementToolPricing(
	pricing: Record<string, number> | undefined,
	storedTools?: MCPOpenAPIPreviewTool[],
	nextTools?: MCPOpenAPIPreviewTool[],
): Record<string, number> | undefined {
	if (!pricing || !nextTools) return pricing;
	const out: Record<string, number> = Object.create(null);
	for (const [name, price] of Object.entries(pricing)) {
		const next = followToolName(name, storedTools, nextTools);
		if (next) out[next] = price;
	}
	return { ...out };
}

/**
 * Why a replacement save must wait, if it must. Auto-execute settings and tool
 * prices follow their operation through the stored document's preview: while
 * that preview loads the save waits, and when it failed the save is held once
 * so the user learns those settings will be dropped, then goes ahead.
 */
export function replacementSaveHold(state: {
	previewingBaseline: boolean;
	baselineFailed: boolean;
	acknowledged: boolean;
}): "pending" | "baseline-failed" | null {
	if (state.previewingBaseline) return "pending";
	if (state.baselineFailed && !state.acknowledged) return "baseline-failed";
	return null;
}

/**
 * The base URL field's error, if any: required when the document gives no
 * usable server (or a custom URL was chosen), and an entered value must be an
 * absolute http(s) URL.
 */
export function openAPIBaseUrlError(
	choice: "server" | "custom",
	preview: MCPOpenAPIPreviewResponse | null | undefined,
	value: string,
): string | undefined {
	const trimmed = value.trim();
	if (!trimmed) return openAPIBaseUrlRequired(choice, preview) ? "Base URL is required" : undefined;
	try {
		const url = new URL(trimmed);
		if ((url.protocol === "http:" || url.protocol === "https:") && url.host) return undefined;
	} catch {
		// fall through
	}
	return "Base URL must be an absolute http:// or https:// URL";
}

/**
 * The preview request for the edit sheet's replacement source. It carries the
 * stored include_deprecated and the client's name, so the picker offers the
 * tools the saved server will build under the names an update accepts.
 */
export function replacePreviewRequest(
	mode: OpenAPISourceMode,
	specText: string,
	specUrl: string,
	customBase: string | undefined,
	includeDeprecated: boolean | undefined,
	clientName?: string,
): MCPOpenAPIPreviewRequest {
	const req: MCPOpenAPIPreviewRequest =
		mode === "url" ? { spec_url: specUrl.trim(), base_url: customBase } : { spec: specText, base_url: customBase };
	if (includeDeprecated) req.include_deprecated = true;
	if (clientName?.trim()) req.client_name = clientName.trim();
	return req;
}
/**
 * Whether loading the stored document failed only because the client has none
 * (a spec_url-only client: the endpoint answers 404). Any other failure is a
 * real error the edit sheet must show rather than an empty editor.
 */
export function isMissingStoredSpec(error: unknown): boolean {
	return typeof error === "object" && error !== null && (error as { status?: unknown }).status === 404;
}

/**
 * The operations a replacement parse reconciles the selection against: the
 * last parsed replacement, or before the first parse, the preview of the
 * stored document (so even the first parse matches by method and path, not by
 * a name another operation may inherit). Undefined when neither exists.
 */
export function replacementPreviousTools(
	latest: MCPOpenAPIPreviewResponse | null | undefined,
	baseline: MCPOpenAPIPreviewResponse | null | undefined,
): MCPOpenAPIPreviewTool[] | undefined {
	return (latest ?? baseline)?.tools;
}