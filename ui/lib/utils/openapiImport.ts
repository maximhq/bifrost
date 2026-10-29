import type { RelayAuthConfig, RelayRouteConfig, RelayRouteMethod } from "@/lib/types/relays";
import { compareRoutes } from "@/lib/utils/relayForm";

// What an OpenAPI or Swagger document resolves into for a relay: the
// operations as routes, the first security scheme as an auth suggestion, and the first server as
// a base URL hint. The document itself is never kept; the operator reviews these fields and saves
// them like hand-typed ones.
export interface OpenAPIImport {
	title?: string;
	version?: string;
	baseUrl?: string;
	auth?: RelayAuthConfig;
	routes: RelayRouteConfig[];
	warnings: string[];
	// Some operations could not be read (a path item behind $ref); they are refused until added by hand.
	partial?: boolean;
}

const ROUTE_METHODS: RelayRouteMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"];

type Doc = Record<string, unknown>;

const asRecord = (v: unknown): Doc | undefined => (v && typeof v === "object" && !Array.isArray(v) ? (v as Doc) : undefined);
const asString = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);

// parseOpenAPIText decodes a JSON or YAML document. YAML support is loaded on demand so the parser
// is not part of the main bundle.
export async function parseOpenAPIText(text: string): Promise<Doc> {
	const trimmed = text.trim();
	if (!trimmed) throw new Error("The document is empty");
	if (trimmed.startsWith("{")) {
		// JSON first; a flow-style YAML document ({openapi: 3.0.0, ...}) also starts with "{" and is
		// parsed as YAML when it is not valid JSON.
		let json: unknown;
		try {
			json = JSON.parse(trimmed);
		} catch {
			json = undefined;
		}
		if (json !== undefined) {
			const parsed = asRecord(json);
			if (!parsed) throw new Error("The document is not a JSON object");
			return parsed;
		}
	}
	const yaml = await import("yaml");
	const parsed = asRecord(yaml.parse(trimmed));
	if (!parsed) throw new Error("The document is not an OpenAPI object");
	return parsed;
}

// Characters the relay's segment matcher (Go path.Match) treats specially; a literal one in a
// template must be escaped or the route matches something else, or nothing.
const GLOB_META = /[*?[\]\\]/g;

// decodeLiteral percent-decodes literal path text; malformed sequences are left untouched.
function decodeLiteral(literal: string): string {
	try {
		return decodeURIComponent(literal);
	} catch {
		return literal;
	}
}

// templateToGlob turns an OpenAPI path template into a route glob: every {param}, whether it is
// the whole segment ({id}) or part of one ({id}.json, {model}:predict), becomes *, and glob
// characters in the literal text are escaped.
export function templateToGlob(template: string): string {
	const rooted = template.startsWith("/") ? template : "/" + template;
	return rooted
		.split("/")
		.map((segment) => {
			// Literal text is matched against the decoded request path, so a percent-encoded template
			// (/files/hello%20world) is decoded first; text that is not valid encoding is kept as is.
			const literals = segment.split(/\{[^}]*\}/).map((literal) => decodeLiteral(literal).replace(GLOB_META, "\\$&"));
			// Adjacent parameters ({name}{ext}) leave an empty literal between them; dropping it makes
			// them one *, since a ** would be refused mid-path and match every path below at the end.
			return literals.filter((literal, i) => i === 0 || i === literals.length - 1 || literal !== "").join("*");
		})
		.join("/");
}

// authFromSecuritySchemes maps the first supported security scheme onto the relay's auth types.
// isEmptyRequirement reports an OpenAPI security alternative with no scheme ({}), which marks
// authentication as optional for the operation it applies to.
const isEmptyRequirement = (requirement: unknown) => Object.keys(asRecord(requirement) ?? {}).length === 0;

// security is the requirement list to read (an operation's own), the document's when omitted.
export function authFromSecuritySchemes(doc: Doc, security: unknown = doc.security): { auth?: RelayAuthConfig; warning?: string } {
	const schemes = asRecord(asRecord(doc.components)?.securitySchemes) ?? asRecord(doc.securityDefinitions);
	if (!schemes) return {};
	const mapScheme = (name: string): { auth?: RelayAuthConfig; warning?: string } => {
		const scheme = asRecord(schemes[name]);
		if (!scheme) return { warning: `Security scheme ${name} is required but not declared; set the auth type by hand` };
		const type = asString(scheme.type)?.toLowerCase();
		if (type === "apikey") {
			const location = asString(scheme.in)?.toLowerCase();
			const paramName = asString(scheme.name) ?? "";
			if (location === "header" && paramName) {
				// An apiKey scheme sends the key verbatim, even in Authorization; only an http bearer
				// scheme adds the "Bearer " prefix.
				return { auth: { type: "header", header_name: paramName } };
			}
			if (location === "query" && paramName) return { auth: { type: "query", query_param: paramName } };
			return {
				warning: `Security scheme ${name} uses an apiKey in ${location ?? "an unsupported location"}, which the relay cannot inject`,
			};
		}
		if (type === "http") {
			const httpScheme = asString(scheme.scheme)?.toLowerCase();
			if (httpScheme === "bearer") return { auth: { type: "bearer" } };
			if (httpScheme === "basic") return { auth: { type: "basic" } };
			return { warning: `Security scheme ${name} uses HTTP ${httpScheme ?? "?"} auth, which the relay cannot inject` };
		}
		if (type === "basic") return { auth: { type: "basic" } };
		return { warning: `Security scheme ${name} is ${type ?? "unknown"}; set the auth type by hand` };
	};
	// The document's top-level security lists alternatives; schemes inside one alternative are
	// required together, which one injected key cannot satisfy. The first alternative that is a
	// single supported scheme wins. Without a requirement, only a lone declared scheme is clear.
	if (Array.isArray(security)) {
		// An empty alternative ({}) makes auth optional: the call works without a key, so no scheme
		// is required of the endpoint.
		if (security.some(isEmptyRequirement))
			return { warning: "The document makes authentication optional; set the auth type by hand if the API needs a key" };
		const warnings: string[] = [];
		for (const requirement of security) {
			const names = Object.keys(asRecord(requirement) ?? {});
			if (names.length !== 1) continue;
			const mapped = mapScheme(names[0]);
			if (mapped.auth) return mapped;
			if (mapped.warning) warnings.push(mapped.warning);
		}
		return {
			warning: warnings[0] ?? "No security alternative in the document is a single scheme the relay can inject; set the auth type by hand",
		};
	}
	const names = Object.keys(schemes);
	if (names.length === 1) return mapScheme(names[0]);
	return { warning: "The document declares several security schemes and requires none; set the auth type by hand" };
}

// baseUrlFromServers reads the first absolute server URL (OpenAPI 3) or host + basePath (Swagger 2).
// With documentUrl (a fetched document), a relative server resolves against it, and a Swagger 2
// document without a host takes the documentUrl's host, as both specifications define.
export function baseUrlFromServers(doc: Doc, documentUrl?: string): string | undefined {
	return serverBase(doc, documentUrl).baseUrl;
}

// serverBase is baseUrlFromServers plus the query the chosen server carried: a relay base URL cannot
// carry one, so it is left out of the suggestion and importOpenAPI warns about it.
function serverBase(doc: Doc, documentUrl?: string): { baseUrl?: string; droppedQuery?: string } {
	const servers = Array.isArray(doc.servers) ? doc.servers : [];
	for (const raw of servers) {
		const url = asString(asRecord(raw)?.url);
		if (!url || url.includes("{")) continue;
		const split = splitUrl(url, /^https?:\/\//i.test(url) ? undefined : documentUrl);
		if (split) return split;
	}
	const host = asString(doc.host);
	if (host) {
		const schemes = Array.isArray(doc.schemes) ? doc.schemes.map(asString) : [];
		const scheme = schemes.includes("https") || schemes.length === 0 ? "https" : schemes[0];
		const basePath = asString(doc.basePath) ?? "";
		return { baseUrl: `${scheme}://${host}${basePath}`.replace(/\/+$/, "") };
	}
	if (doc.swagger !== undefined) return { baseUrl: swaggerBaseFromDocument(doc, documentUrl) };
	// OpenAPI 3: with no servers (or an empty list) the server is "/", that is, the document's own origin.
	if (doc.openapi !== undefined && servers.length === 0) return { baseUrl: resolveAgainst("/", documentUrl) };
	return {};
}

// A Swagger 2 document without a host is served from the API's own host, so only the host comes from
// documentUrl; a declared scheme still wins (https when both are listed), else the document's.
function swaggerBaseFromDocument(doc: Doc, documentUrl: string | undefined): string | undefined {
	const resolved = resolveAgainst(asString(doc.basePath) ?? "/", documentUrl);
	if (!resolved) return undefined;
	const schemes = (Array.isArray(doc.schemes) ? doc.schemes.map(asString) : []).filter((s) => s === "http" || s === "https");
	if (schemes.length === 0) return resolved;
	const scheme = schemes.includes("https") ? "https" : schemes[0];
	const url = new URL(resolved);
	url.protocol = `${scheme}:`;
	return `${url.origin}${url.pathname}`.replace(/\/+$/, "");
}

function resolveAgainst(url: string, documentUrl: string | undefined): string | undefined {
	return documentUrl ? splitUrl(url, documentUrl)?.baseUrl : undefined;
}

// splitUrl parses an absolute URL, or a relative one against base, into the origin and path a relay
// base URL can hold and the query it cannot. A relative URL without a base, or a non-http(s) one, is
// undefined.
function splitUrl(url: string, base: string | undefined): { baseUrl: string; droppedQuery?: string } | undefined {
	try {
		const parsed = base ? new URL(url, base) : new URL(url);
		if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
		return { baseUrl: `${parsed.origin}${parsed.pathname}`.replace(/\/+$/, ""), ...(parsed.search ? { droppedQuery: parsed.search } : {}) };
	} catch {
		return undefined;
	}
}

// AWS specifications key every operation of a JSON-protocol API as "/#X-Amz-Target=Service.Op": the
// operation is a header, not a path. Such a key becomes a route on the path before the "#" with a
// header matcher, which is what the relay needs to tell the operations apart.
export function templateToRoute(template: string): { path: string; headers?: Record<string, string> } {
	const hash = template.indexOf("#");
	if (hash < 0) return { path: templateToGlob(template) };
	const base = template.slice(0, hash) || "/";
	const fragment = template.slice(hash + 1);
	const eq = fragment.indexOf("=");
	if (eq <= 0) return { path: templateToGlob(base) };
	return { path: templateToGlob(base), headers: { [fragment.slice(0, eq)]: fragment.slice(eq + 1) } };
}

// importOpenAPI resolves a parsed document into endpoint fields.
export function importOpenAPI(doc: Doc, documentUrl?: string): OpenAPIImport {
	const info = asRecord(doc.info);
	const result: OpenAPIImport = { title: asString(info?.title), version: asString(info?.version), routes: [], warnings: [] };
	const paths = asRecord(doc.paths);
	// The requirement list each operation is under: its own security when it sets one, else the
	// document's. An empty list marks a public operation, which needs no key.
	const requirements: unknown[] = [];
	if (!paths) {
		result.warnings.push("The document declares no paths");
	} else {
		const referenced: string[] = [];
		for (const [template, rawItem] of Object.entries(paths)) {
			const item = asRecord(rawItem);
			if (!item) continue;
			if (typeof item.$ref === "string") {
				referenced.push(template);
				continue;
			}
			for (const method of Object.keys(item)) {
				const upper = method.toUpperCase() as RelayRouteMethod;
				if (!ROUTE_METHODS.includes(upper)) continue;
				result.routes.push({ method: upper, ...templateToRoute(template) });
				const own = asRecord(item[method])?.security;
				const effective = Array.isArray(own) ? own : doc.security;
				if (!Array.isArray(effective) || !(effective.length === 0 || effective.some(isEmptyRequirement))) requirements.push(effective);
			}
		}
		// The relay takes the first route that matches; compareRoutes is the order every save uses.
		result.routes.sort(compareRoutes);
		if (referenced.length > 0) {
			result.partial = true;
			result.warnings.push(
				`${referenced.join(", ")} ${referenced.length === 1 ? "is" : "are"} defined by $ref, which the import does not resolve, so those operations were not imported; add their routes by hand and review the routes list.`,
			);
		}
		if (result.routes.length === 0 && referenced.length === 0)
			result.warnings.push("No operation in the document uses a method the relay serves");
	}
	// One endpoint injects one auth, so it is set only when every operation that needs a key agrees.
	// When operations were read and none needs a key, nothing is inferred: the document's default
	// scheme would be an auth no imported call uses. The default is read only when no routes were.
	const candidates =
		requirements.length > 0
			? requirements.map((r) => authFromSecuritySchemes(doc, r))
			: result.routes.length > 0
				? []
				: [authFromSecuritySchemes(doc)];
	const first = candidates[0] ?? {};
	const agree = candidates.every((c) => JSON.stringify(c.auth) === JSON.stringify(first.auth));
	if (!agree) {
		result.warnings.push("The operations require different security schemes, and one endpoint applies one auth; set the auth type by hand");
	} else {
		if (first.auth) result.auth = first.auth;
		if (first.warning) result.warnings.push(first.warning);
	}
	const server = serverBase(doc, documentUrl);
	result.baseUrl = server.baseUrl;
	if (server.droppedQuery)
		result.warnings.push(
			`The server URL's query (${server.droppedQuery}) is left out of the suggested base URL, since a relay base URL cannot carry one; callers must send it themselves.`,
		);
	return result;
}