import type { GenericAuthConfig, GenericRouteConfig, GenericRouteMethod } from "@/lib/types/config";

// What an OpenAPI or Swagger document resolves into for a generic passthrough provider: the
// operations as routes, the first security scheme as an auth suggestion, and the first server as
// a base URL hint. The document itself is never kept; the operator reviews these fields and saves
// them like hand-typed ones.
export interface OpenAPIImport {
	title?: string;
	version?: string;
	baseUrl?: string;
	auth?: GenericAuthConfig;
	routes: GenericRouteConfig[];
	warnings: string[];
}

const ROUTE_METHODS: GenericRouteMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"];

type Doc = Record<string, unknown>;

const asRecord = (v: unknown): Doc | undefined => (v && typeof v === "object" && !Array.isArray(v) ? (v as Doc) : undefined);
const asString = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);

// parseOpenAPIText decodes a JSON or YAML document. YAML support is loaded on demand so the parser
// is not part of the main bundle.
export async function parseOpenAPIText(text: string): Promise<Doc> {
	const trimmed = text.trim();
	if (!trimmed) throw new Error("The document is empty");
	if (trimmed.startsWith("{")) {
		const parsed = asRecord(JSON.parse(trimmed));
		if (!parsed) throw new Error("The document is not a JSON object");
		return parsed;
	}
	const yaml = await import("yaml");
	const parsed = asRecord(yaml.parse(trimmed));
	if (!parsed) throw new Error("The document is not an OpenAPI object");
	return parsed;
}

// Characters the relay's segment matcher (Go path.Match) treats specially; a literal one in a
// template must be escaped or the route matches something else, or nothing.
const GLOB_META = /[*?[\]\\]/g;

// templateToGlob turns an OpenAPI path template into a route glob: every {param}, whether it is
// the whole segment ({id}) or part of one ({id}.json, {model}:predict), becomes *, and glob
// characters in the literal text are escaped.
export function templateToGlob(template: string): string {
	const rooted = template.startsWith("/") ? template : "/" + template;
	return rooted
		.split("/")
		.map((segment) =>
			segment
				.split(/\{[^}]*\}/)
				.map((literal) => literal.replace(GLOB_META, "\\$&"))
				.join("*"),
		)
		.join("/");
}

// authFromSecuritySchemes maps the first supported security scheme onto the relay's auth types.
export function authFromSecuritySchemes(doc: Doc): { auth?: GenericAuthConfig; warning?: string } {
	const schemes = asRecord(asRecord(doc.components)?.securitySchemes) ?? asRecord(doc.securityDefinitions);
	if (!schemes) return {};
	for (const [name, raw] of Object.entries(schemes)) {
		const scheme = asRecord(raw);
		if (!scheme) continue;
		const type = asString(scheme.type)?.toLowerCase();
		if (type === "apikey") {
			const location = asString(scheme.in)?.toLowerCase();
			const paramName = asString(scheme.name) ?? "";
			if (location === "header" && paramName) {
				return { auth: paramName.toLowerCase() === "authorization" ? { type: "bearer" } : { type: "header", header_name: paramName } };
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
	}
	return {};
}

// baseUrlFromServers reads the first absolute server URL (OpenAPI 3) or host + basePath (Swagger 2).
export function baseUrlFromServers(doc: Doc): string | undefined {
	const servers = Array.isArray(doc.servers) ? doc.servers : [];
	for (const raw of servers) {
		const url = asString(asRecord(raw)?.url);
		if (url && /^https?:\/\//i.test(url) && !url.includes("{")) return url.replace(/\/+$/, "");
	}
	const host = asString(doc.host);
	if (host) {
		const schemes = Array.isArray(doc.schemes) ? doc.schemes.map(asString) : [];
		const scheme = schemes.includes("https") || schemes.length === 0 ? "https" : schemes[0];
		const basePath = asString(doc.basePath) ?? "";
		return `${scheme}://${host}${basePath}`.replace(/\/+$/, "");
	}
	return undefined;
}

// importOpenAPI resolves a parsed document into provider fields.
export function importOpenAPI(doc: Doc): OpenAPIImport {
	const info = asRecord(doc.info);
	const result: OpenAPIImport = { title: asString(info?.title), version: asString(info?.version), routes: [], warnings: [] };
	const paths = asRecord(doc.paths);
	if (!paths) {
		result.warnings.push("The document declares no paths");
	} else {
		for (const [template, rawItem] of Object.entries(paths)) {
			const item = asRecord(rawItem);
			if (!item) continue;
			for (const method of Object.keys(item)) {
				const upper = method.toUpperCase() as GenericRouteMethod;
				if (!ROUTE_METHODS.includes(upper)) continue;
				result.routes.push({ method: upper, path: templateToGlob(template) });
			}
		}
		if (result.routes.length === 0) result.warnings.push("No operation in the document uses a method the relay serves");
	}
	const { auth, warning } = authFromSecuritySchemes(doc);
	if (auth) result.auth = auth;
	if (warning) result.warnings.push(warning);
	result.baseUrl = baseUrlFromServers(doc);
	return result;
}