import i18n from "@/lib/i18n";
import { withBasePath } from "@/lib/utils/basePath";
import type { Relay, RelayJobConfig, RelayRouteConfig, RelaySecret, RelayUsageConfig } from "@/lib/types/relays";
import {
	isJSONText,
	type RelayFormValues,
	type RelayUsageForm,
	relayAuthSchema,
	relayNetworkFormSchema,
	relayUsageBaseSchema,
	routeBillableByDefault,
} from "@/lib/types/schemas";

type FormSecret = { value?: string; ref?: string; type?: "plain_text" | "env" | "vault" };
type FormUsage = RelayFormValues["usage"];

/**
 * An object with every key of a form section, all unset. The config sheet's fields are controlled,
 * and registering one writes its (undefined) key into the form's values; a reset whose defaults lack
 * that key then leaves the form reading as dirty, since react-hook-form compares key sets, and Save
 * stays enabled after a save. Defaults built from these carry the same keys as the registered fields.
 */
function allKeysUnset<T extends object>(shape: object): T {
	const out: Record<string, undefined> = {};
	for (const key of Object.keys(shape)) out[key] = undefined;
	return out as T;
}
export const emptyUsageForm = (): FormUsage => allKeysUnset<FormUsage>(relayUsageBaseSchema.shape);
export const emptyNetworkForm = (): RelayFormValues["network"] => allKeysUnset<RelayFormValues["network"]>(relayNetworkFormSchema.shape);
const emptyAuthForm = (): Omit<RelayFormValues["auth"], "type"> =>
	allKeysUnset<Omit<RelayFormValues["auth"], "type">>(relayAuthSchema.shape);

const toFormSecret = (s?: RelaySecret): FormSecret | undefined =>
	s ? { value: s.value, ref: s.ref, type: s.type as FormSecret["type"] } : undefined;

const toApiSecret = (s?: FormSecret): RelaySecret | undefined => {
	if (!s || (!s.value?.trim() && !s.ref?.trim())) return undefined;
	return { value: s.value ?? "", ...(s.ref ? { ref: s.ref } : {}), ...(s.type ? { type: s.type } : {}) };
};

type RouteOrderKey = Pick<RelayRouteConfig, "method" | "path" | "headers">;

/** Whether a path segment is a glob (isRouteGlob in core/relay). */
export const isRouteGlob = (segment: string) => /[*?[]/.test(segment);
/** Escapes text so a glob matches it literally. */
export const escapeGlob = (text: string) => text.replace(/[*?[\]\\]/g, "\\$&");
const codePointOrder = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

/** The header matchers a save keeps: named, with a value. */
export const savedHeaders = (headers: { name: string; value: string }[]): Record<string, string> =>
	Object.fromEntries(headers.filter((h) => h.name.trim() && h.value.trim()).map((h) => [h.name.trim(), h.value.trim()]));

/** What identifies a route, since routes have no id: its method, path and header matchers. */
export function routeKey(r: { method: string; path: string; headers: { name: string; value: string }[] }): string {
	return [
		`${r.method.toUpperCase()} ${r.path}`,
		...r.headers
			.filter((h) => h.name.trim())
			.map((h) => `${h.name.trim().toLowerCase()}: ${h.value.trim()}`)
			.sort(),
	].join(" ");
}

/** Where an opened route is now, as the list can refetch in a new order; -1 when it is gone. */
export function locateRoute<T extends Parameters<typeof routeKey>[0]>(routes: T[], index: number, key: string): number {
	if (routes[index] && routeKey(routes[index]) === key) return index;
	return routes.findIndex((r) => routeKey(r) === key);
}

/** The sort key of a route path; see matchOrder. */
function pathRank(path: string): number[] {
	const segments = path.replace(/^\//, "").split("/");
	const catchAll = segments[segments.length - 1] === "**";
	const gap = !catchAll && segments.includes("**");
	const fixed = segments.filter((s) => s !== "**");
	const glob = fixed.some(isRouteGlob);
	// Equal literal text, fewer and narrower wildcards first: /x/foo* is covered by /x/*foo*, and
	// /files/[m] by /files/[a-z] by /files/? by /files/*.
	const shape = globShape(fixed.join("/"));
	return [
		catchAll ? 2 : gap ? 1 : 0,
		catchAll || gap ? -fixed.length : 0,
		glob ? 1 : 0,
		glob ? fixed.length : 0,
		glob ? -(shape.literal + shape.positions) : 0,
		glob ? shape.stars : 0,
		glob ? shape.singles : 0,
	];
}

/** Paths in the order the gateway tries them (compareRoutePaths in core/relay); equal ranks keep their saved order. */
export function comparePathRank(a: string, b: string): number {
	const ra = pathRank(a);
	const rb = pathRank(b);
	for (let i = 0; i < ra.length; i++) if (ra[i] !== rb[i]) return ra[i] - rb[i];
	return 0;
}

// How many characters a ? (or a negated class) can stand for, for ranking only.
const ANY_CHARACTER = 0x110000;

/**
 * The shape of a path.Match pattern, as the gateway reads it (routeGlobShape): literal characters
 * (an escaped one included), single-character positions (each ? or class, fixed width like a
 * literal), * wildcards, and the breadth of those positions: a class counts the characters it
 * matches ([m] 1, [a-z] 26, a negated one nearly all), and a ? all.
 */
function globShape(source: string): { literal: number; positions: number; stars: number; singles: number } {
	// By code point, as the gateway reads runes: an emoji is one character.
	const pattern = Array.from(source);
	let literal = 0;
	let stars = 0;
	let singles = 0;
	let positions = 0;
	// A run of * counts once, since a* and a** match the same paths.
	let star = false;
	for (let i = 0; i < pattern.length; i++) {
		const c = pattern[i];
		const run = star;
		star = false;
		if (c === "\\" && i + 1 < pattern.length) {
			literal++;
			i++;
		} else if (c === "*") {
			if (!run) stars++;
			star = true;
		} else if (c === "?") {
			positions++;
			singles += ANY_CHARACTER;
		} else if (c === "[") {
			positions++;
			const negated = pattern[i + 1] === "^";
			let j = negated ? i + 2 : i + 1;
			let size = 0;
			for (; j < pattern.length && pattern[j] !== "]"; j++) {
				let lo = pattern[j];
				if (lo === "\\" && j + 1 < pattern.length) lo = pattern[++j];
				if (pattern[j + 1] === "-" && j + 2 < pattern.length && pattern[j + 2] !== "]") {
					let hi = pattern[j + 2];
					j += 2;
					if (hi === "\\" && j + 1 < pattern.length) hi = pattern[++j];
					size += Math.max(1, hi.codePointAt(0)! - lo.codePointAt(0)! + 1);
				} else size++;
			}
			singles += negated ? ANY_CHARACTER - size : size;
			i = j;
		} else literal++;
	}
	return { literal, positions, stars, singles };
}

/** Go's path.Match, by code point: * and ? stop at "/", [...] is a class, \ escapes. A malformed pattern matches nothing. */
export function globMatch(patternText: string, valueText: string): boolean {
	const pattern = Array.from(patternText);
	const value = Array.from(valueText);
	const memo = new Map<string, boolean>();
	const at = (p: number, v: number): boolean => {
		const key = `${p},${v}`;
		const known = memo.get(key);
		if (known !== undefined) return known;
		let result: boolean;
		if (p === pattern.length) result = v === value.length;
		else if (pattern[p] === "*") result = at(p + 1, v) || (v < value.length && value[v] !== "/" && at(p, v + 1));
		else if (v === value.length) result = false;
		else if (pattern[p] === "?") result = value[v] !== "/" && at(p + 1, v + 1);
		else if (pattern[p] === "[") {
			const cls = matchClass(pattern, p, value[v]);
			result = cls !== null && cls.matched && at(cls.end, v + 1);
		} else if (pattern[p] === "\\") result = p + 1 < pattern.length && pattern[p + 1] === value[v] && at(p + 2, v + 1);
		else result = pattern[p] === value[v] && at(p + 1, v + 1);
		memo.set(key, result);
		return result;
	};
	return at(0, 0);
}

/** One class character, as path.Match's getEsc reads it; null when the class is malformed. */
function classChar(pattern: string[], at: number): { code: number; next: number } | null {
	let i = at;
	if (i >= pattern.length || pattern[i] === "-" || pattern[i] === "]") return null;
	if (pattern[i] === "\\" && ++i >= pattern.length) return null;
	if (i + 1 >= pattern.length) return null;
	return { code: pattern[i].codePointAt(0)!, next: i + 1 };
}

function matchClass(pattern: string[], start: number, c: string): { matched: boolean; end: number } | null {
	const code = c.codePointAt(0)!;
	let i = start + 1;
	const negated = pattern[i] === "^";
	if (negated) i++;
	let matched = false;
	for (let terms = 0; ; terms++) {
		if (terms > 0 && pattern[i] === "]") return { matched: matched !== negated, end: i + 1 };
		const lo = classChar(pattern, i);
		if (!lo) return null;
		let hi = lo;
		i = lo.next;
		if (pattern[i] === "-") {
			const end = classChar(pattern, i + 1);
			if (!end) return null;
			hi = end;
			i = end.next;
		}
		if (lo.code <= code && code <= hi.code) matched = true;
	}
}

/** Whether a header matcher is a glob (isHeaderGlob in core/relay). */
export const isHeaderGlob = (value: string) => /[*?[\\]/.test(value);
/** A header value against its glob; / is swapped out so * can span it, as in core/relay. */
export const headerGlobMatches = (pattern: string, value: string) =>
	globMatch(pattern.replaceAll("/", "\u0001"), value.replaceAll("/", "\u0001"));

/** True only when containment is proven: under *, an exact value under its glob, or a longer fixed start under a trailing-* glob. */
function headerValueNarrower(narrow: string, broad: string): boolean {
	if (!isHeaderGlob(broad)) return false;
	if (broad === "*") return true;
	if (!isHeaderGlob(narrow)) return headerGlobMatches(broad, narrow);
	if (!broad.endsWith("*")) return false;
	const prefix = broad.slice(0, -1);
	return !isHeaderGlob(prefix) && narrow.slice(0, narrow.search(/[*?[\\]/)).startsWith(prefix);
}

type HeaderMap = Map<string, string>;

/** Turns a run of * into one, leaving escapes and character classes as written. */
function collapseStars(pattern: string): string {
	if (!pattern.includes("**")) return pattern;
	let out = "";
	let star = false;
	for (let i = 0; i < pattern.length; i++) {
		const c = pattern[i];
		if (c === "*") {
			if (!star) out += c;
			star = true;
			continue;
		}
		star = false;
		out += c;
		if (c === "\\" && i + 1 < pattern.length) out += pattern[++i];
		else if (c === "[") {
			for (i++; i < pattern.length; i++) {
				out += pattern[i];
				if (pattern[i] === "\\" && i + 1 < pattern.length) out += pattern[++i];
				else if (pattern[i] === "]") break;
			}
		}
	}
	return out;
}

/** Whether every request a matches provably also fits b, and a is stricter. */
function headersNarrower(a: HeaderMap, b: HeaderMap): boolean {
	if (a.size < b.size) return false;
	let strict = a.size > b.size;
	for (const [name, value] of b) {
		const own = a.get(name);
		if (own === undefined) return false;
		// a* and a** match the same values, so neither is narrower.
		const narrow = collapseStars(own);
		const broad = collapseStars(value);
		if (narrow === broad) continue;
		if (!headerValueNarrower(narrow, broad)) return false;
		strict = true;
	}
	return strict;
}

/** An all-exact a whose value falls under a wildcard of b on the same header, with every other shared header accepting a too. */
function exactOverlapsWildcard(a: HeaderMap, b: HeaderMap): boolean {
	if (a.size === 0 || ![...b.values()].some(isHeaderGlob) || [...a.values()].some(isHeaderGlob)) return false;
	let underWildcard = false;
	for (const [name, value] of a) {
		const pattern = b.get(name);
		if (pattern === undefined) continue;
		if (!headerGlobMatches(pattern, value)) return false;
		underWildcard ||= isHeaderGlob(pattern);
	}
	return underWildcard;
}

const headersGoBefore = (a: HeaderMap, b: HeaderMap) => headersNarrower(a, b) || (exactOverlapsWildcard(a, b) && !headersNarrower(b, a));

/** Each route labelled with the first route of the preference cycle it belongs to, or itself. */
function cycleGroups(goes: boolean[][]): number[] {
	const n = goes.length;
	const reach = goes.map((row) => [...row]);
	for (let m = 0; m < n; m++)
		for (let a = 0; a < n; a++) {
			if (!reach[a][m]) continue;
			for (let b = 0; b < n; b++) if (reach[m][b]) reach[a][b] = true;
		}
	return goes.map((_, k) => {
		for (let j = 0; j < k; j++) if (reach[j][k] && reach[k][j]) return j;
		return k;
	});
}

/**
 * Keeps the saved order and moves a route only ahead of one it must beat: the routes that must go
 * before a route are placed just before it. Routes in a preference cycle stay together in saved order.
 */
function orderOnePath(routes: RouteOrderKey[], members: number[]): number[] {
	const headers = members.map((i) => new Map(Object.entries(routes[i].headers ?? {}).map(([name, value]) => [name.toLowerCase(), value])));
	const goes = headers.map((a, x) => headers.map((b, y) => x !== y && headersGoBefore(a, b)));
	const group = cycleGroups(goes);
	const n = members.length;
	const placed = members.map(() => false);
	const out: number[] = [];
	const place = (k: number) => {
		for (let j = 0; j < n; j++) if (group[j] === group[k]) placed[j] = true;
		for (let p = 0; p < n; p++) {
			if (placed[p]) continue;
			for (let j = 0; j < n; j++) {
				if (group[j] === group[k] && goes[p][j]) {
					place(p);
					break;
				}
			}
		}
		for (let j = 0; j < n; j++) if (group[j] === group[k]) out.push(members[j]);
	};
	for (let k = 0; k < n; k++) if (!placed[k]) place(k);
	return out;
}

/**
 * Route indices in the order the gateway tries them (routeMatchOrder in core/relay). Specific paths
 * come first and wildcards last, and equal ranks keep their saved order. Routes with one method and
 * path then reorder among their own positions: a route proven narrower goes first, an exact header
 * value goes before a wildcard that can also take its requests, and nothing else moves.
 */
export function matchOrder(routes: RouteOrderKey[]): number[] {
	const order = routes.map((_, i) => i).sort((a, b) => comparePathRank(routes[a].path, routes[b].path) || a - b);
	const slots = new Map<string, number[]>();
	order.forEach((i, at) => {
		const key = `${routes[i].method.toUpperCase()} ${routes[i].path}`;
		slots.set(key, [...(slots.get(key) ?? []), at]);
	});
	for (const positions of slots.values()) {
		if (positions.length < 2) continue;
		orderOnePath(
			routes,
			positions.map((at) => order[at]),
		).forEach((i, k) => {
			order[positions[k]] = i;
		});
	}
	return order;
}

/** Route indices in the order a save writes them: match order, with literal paths A to Z, which never overlap. */
export function saveOrder(routes: RouteOrderKey[]): number[] {
	const literal = (path: string) => pathRank(path).every((n) => n === 0);
	// A \ escape matches the plain character, so /jobs/\status and /jobs/status sort as one path and keep their saved order.
	const unescaped = (path: string) => path.replace(/\\(.)/g, "$1");
	const byPath = routes
		.map((_, i) => i)
		.sort((a, b) => {
			const rank = comparePathRank(routes[a].path, routes[b].path);
			if (rank !== 0 || !literal(routes[a].path)) return rank || a - b;
			return (
				codePointOrder(unescaped(routes[a].path), unescaped(routes[b].path)) ||
				codePointOrder(routes[a].method.toUpperCase(), routes[b].method.toUpperCase()) ||
				a - b
			);
		});
	return matchOrder(byPath.map((i) => routes[i])).map((k) => byPath[k]);
}

export const orderRoutes = <T extends RouteOrderKey>(routes: T[]): T[] => saveOrder(routes).map((i) => routes[i]);

export function emptyRelayForm(): RelayFormValues {
	return {
		name: "",
		description: "",
		base_url: "",
		auth: { ...emptyAuthForm(), type: "bearer" },
		credential_value: { value: "" },
		aws: {},
		extra_headers: [],
		network: emptyNetworkForm(),
		proxy: { type: "inherit" },
		usage: emptyUsageForm(),
		streaming: { paths: [] },
		routes: [],
		skip_route_check: false,
		ignore_upstream_cost: false,
		body_overrides: [],
	};
}

type BodyOverridePair = { name: string; value: string };

/** Forced body fields as the form's path/JSON-text pairs. */
export function toBodyOverridePairs(overrides: Record<string, unknown> | undefined): BodyOverridePair[] {
	return Object.entries(overrides ?? {}).map(([name, value]) => ({ name, value: JSON.stringify(value) }));
}

/** The form's pairs as the API's map; blank rows are skipped, the schema has refused invalid ones. */
export function toBodyOverrides(pairs: BodyOverridePair[] | undefined): Record<string, unknown> | undefined {
	// No prototype, so a __proto__ path is stored as a key instead of hitting the inherited setter.
	const out: Record<string, unknown> = Object.create(null);
	for (const pair of pairs ?? []) {
		const name = pair.name.trim();
		if (!name || !isJSONText(pair.value)) continue;
		out[name] = JSON.parse(pair.value);
	}
	return Object.keys(out).length ? out : undefined;
}

/** Turns an endpoint read from the API (secrets redacted) into form values. */
export function toRelayForm(e: Relay): RelayFormValues {
	return {
		name: e.name,
		description: e.description ?? "",
		base_url: e.base_url,
		auth: { ...emptyAuthForm(), ...e.auth, type: e.auth?.type || "bearer" }, // an omitted type is stored as "" and means bearer
		credential_value: toFormSecret(e.credential?.value) ?? { value: "" },
		aws: {
			access_key: toFormSecret(e.credential?.aws?.access_key),
			secret_key: toFormSecret(e.credential?.aws?.secret_key),
			session_token: toFormSecret(e.credential?.aws?.session_token),
			region: toFormSecret(e.credential?.aws?.region),
			role_arn: toFormSecret(e.credential?.aws?.role_arn),
			external_id: toFormSecret(e.credential?.aws?.external_id),
			session_name: toFormSecret(e.credential?.aws?.session_name),
		},
		extra_headers: Object.entries(e.extra_headers ?? {}).map(([name, value]) => ({ name, value: toFormSecret(value) ?? { value: "" } })),
		network: { ...emptyNetworkForm(), ...(e.network ?? {}) },
		proxy: {
			type: e.proxy_config ? (e.proxy_config.type ?? "none") : "inherit",
			url: toFormSecret(e.proxy_config?.url),
			username: toFormSecret(e.proxy_config?.username),
			password: toFormSecret(e.proxy_config?.password),
			ca_cert_pem: toFormSecret(e.proxy_config?.ca_cert_pem),
		},
		usage: { ...emptyUsageForm(), ...(cleanUsage(e.usage, false) ?? {}) },
		streaming: {
			paths: e.streaming?.paths ?? [],
		},
		routes: (e.routes ?? []).map((r) => ({
			method: r.method.toUpperCase() as RelayRouteConfig["method"], // the server accepts any case
			path: r.path,
			headers: Object.entries(r.headers ?? {}).map(([name, value]) => ({ name, value })),
			enabled: r.enabled ?? true,
			billable: r.billable,
			stream: r.stream,
			usage: r.usage ? { ...r.usage } : undefined,
			job: r.job,
			body_overrides: toBodyOverridePairs(r.body_overrides),
			inherit_body_overrides: r.inherit_body_overrides,
		})),
		skip_route_check: e.skip_route_check ?? false,
		ignore_upstream_cost: e.ignore_upstream_cost ?? false,
		body_overrides: toBodyOverridePairs(e.body_overrides),
	};
}

/**
 * Drops empty fields from a usage mapping. At the endpoint level a zero rate or fee means "none",
 * so it is dropped. On a route a zero is an explicit override that waives the endpoint's value, so
 * it is kept; an empty route field inherits.
 */
export function cleanUsage(u: FormUsage | undefined, route: boolean): RelayUsageConfig | undefined {
	if (!u) return undefined;
	const out: RelayUsageConfig = {};
	for (const [k, v] of Object.entries(u) as [keyof RelayUsageConfig, unknown][]) {
		if (v === undefined || v === null || v === "") continue;
		if (typeof v === "number" && Number.isNaN(v)) continue;
		// At the relay level a field at its default is unset, so the form shows the placeholder.
		if (!route && typeof v === "number" && v === 0 && (k === "cost_per_request" || k === "cost_per_unit" || k === "units_minimum"))
			continue;
		// A block size is a price: 0.5 bills 4 raw units as 8 blocks and 1 rounds fractions up, so any
		// positive value is kept; only an unset (zero) one is dropped.
		if (!route && k === "units_block_size" && typeof v === "number" && v <= 0) continue;
		(out as Record<string, unknown>)[k] = typeof v === "string" ? v.trim() : v;
	}
	return Object.keys(out).length ? out : undefined;
}

export { routeBillableByDefault };

// What prices a call. A route that is not billed drops these; the model and token paths stay, since
// the gateway reads them on every call to log the model and tokens.
const PRICING_FIELDS = [
	"cost_per_request",
	"cost_per_unit",
	"cost_path",
	"units_path",
	"units_source",
	"units_measure",
	"units_block_size",
	"units_minimum",
] as const;

/** The usage mapping without its pricing fields, or undefined when nothing else is set. */
export function withoutPricing<T extends Partial<Record<(typeof PRICING_FIELDS)[number], unknown>>>(usage: T | undefined): T | undefined {
	if (!usage) return undefined;
	const rest = { ...usage };
	for (const field of PRICING_FIELDS) delete rest[field];
	return Object.values(rest).some((v) => v !== undefined && v !== null && v !== "") ? rest : undefined;
}

/** Turns form values into the endpoint body the API takes. */
export function toRelayConfig(v: RelayFormValues): Relay {
	const authType = v.auth.type;
	const auth = {
		type: authType,
		...(authType === "header"
			? { header_name: v.auth.header_name, ...(v.auth.value_prefix ? { value_prefix: v.auth.value_prefix } : {}) }
			: {}),
		...(authType === "query" ? { query_param: v.auth.query_param } : {}),
		...(authType === "aws_sigv4" ? { aws: { service: v.auth.aws?.service ?? "" } } : {}),
	};
	let credential: Relay["credential"];
	if (authType === "aws_sigv4") {
		credential = {
			aws: {
				access_key: toApiSecret(v.aws?.access_key),
				secret_key: toApiSecret(v.aws?.secret_key),
				session_token: toApiSecret(v.aws?.session_token),
				region: toApiSecret(v.aws?.region),
				role_arn: toApiSecret(v.aws?.role_arn),
				external_id: toApiSecret(v.aws?.external_id),
				session_name: toApiSecret(v.aws?.session_name),
			},
		};
	} else if (authType !== "none") {
		credential = { value: toApiSecret(v.credential_value) };
	}
	const extraHeaders: Record<string, RelaySecret> = {};
	for (const h of v.extra_headers) {
		const value = toApiSecret(h.value);
		if (h.name.trim() && value) extraHeaders[h.name.trim()] = value;
	}
	const network = Object.fromEntries(
		Object.entries(v.network).filter(
			([, x]) => x !== undefined && x !== "" && x !== false && !(typeof x === "number" && (x === 0 || Number.isNaN(x))),
		),
	);
	const streamingSet = v.streaming.paths.length > 0;
	return {
		name: v.name.trim(),
		description: v.description?.trim() || undefined,
		base_url: v.base_url.trim(),
		auth,
		credential,
		extra_headers: Object.keys(extraHeaders).length ? extraHeaders : undefined,
		network: Object.keys(network).length ? network : undefined,
		// "none" names no proxy, the same as leaving it unset (the provider rule): both inherit the
		// global proxy, so both are saved as no proxy_config.
		proxy_config:
			v.proxy.type === "inherit" || v.proxy.type === "none"
				? undefined
				: {
						type: v.proxy.type,
						url: toApiSecret(v.proxy.url),
						username: toApiSecret(v.proxy.username),
						password: toApiSecret(v.proxy.password),
						ca_cert_pem: toApiSecret(v.proxy.ca_cert_pem),
					},
		usage: cleanUsage(v.usage, false),
		streaming: streamingSet ? { ...v.streaming, paths: v.streaming.paths.filter((p) => p.trim()) } : undefined,
		routes: orderRoutes(
			v.routes.map((r) => {
				const headers = savedHeaders(r.headers);
				// A route that is not billed is never priced and hides its pricing, so only the model and
				// token mapping the gateway still reads on every call is sent.
				const usage =
					(r.billable ?? routeBillableByDefault(r.method)) ? cleanUsage(r.usage, true) : withoutPricing(cleanUsage(r.usage, true));
				const bodyOverrides = toBodyOverrides(r.body_overrides);
				return {
					method: r.method,
					path: r.path.trim(),
					...(Object.keys(headers).length ? { headers } : {}),
					...(r.enabled === false ? { enabled: false } : {}),
					...(r.billable !== undefined && r.billable !== routeBillableByDefault(r.method) ? { billable: r.billable } : {}),
					...(r.stream !== undefined ? { stream: r.stream } : {}),
					...(usage ? { usage } : {}),
					...(r.job ? { job: cleanJob(r.job) } : {}),
					...(bodyOverrides ? { body_overrides: bodyOverrides } : {}),
					...(r.inherit_body_overrides === false ? { inherit_body_overrides: false } : {}),
				};
			}),
		),
		skip_route_check: v.skip_route_check || undefined,
		ignore_upstream_cost: v.ignore_upstream_cost || undefined,
		body_overrides: toBodyOverrides(v.body_overrides),
	};
}

/** Drops a job's empty optional fields so the stored config carries only what was set. */
export function cleanJob(
	job: Omit<RelayJobConfig, "usage" | "interval_seconds" | "timeout_seconds"> & {
		usage?: RelayUsageForm;
		interval_seconds?: number | null;
		timeout_seconds?: number | null;
	},
): RelayJobConfig {
	const poll = {
		...(job.poll.method ? { method: job.poll.method } : {}),
		path: job.poll.path.trim(),
		...(job.poll.headers && Object.keys(job.poll.headers).length ? { headers: job.poll.headers } : {}),
		...(job.poll.body ? { body: job.poll.body } : {}),
		...(job.poll.content_type ? { content_type: job.poll.content_type } : {}),
	};
	const usage = cleanUsage(job.usage, true);
	return {
		id_path: job.id_path.trim(),
		poll,
		status_path: job.status_path.trim(),
		succeeded: job.succeeded,
		...(job.failed?.length ? { failed: job.failed } : {}),
		...(job.interval_seconds ? { interval_seconds: job.interval_seconds } : {}),
		...(job.timeout_seconds ? { timeout_seconds: job.timeout_seconds } : {}),
		...(usage ? { usage } : {}),
		...(job.reused_id_is_new_job ? { reused_id_is_new_job: true } : {}),
	};
}

/** The path callers reach a relay at, for display and copying. The server sends it root-relative, so the server.base_path prefix goes on here. */
export function relayGatewayPath(relay: Pick<Relay, "name" | "url">): string {
	return withBasePath(relay.url || `/relay/${relay.name}`);
}

/** A one-line summary of what a call costs under a mapping, for the route table and pricing tab. */
// ignoreUpstreamCost drops the reported cost, as the server does when the relay ignores it.
export function describePricing(u: RelayUsageConfig | undefined, ignoreUpstreamCost = false): string {
	const T = { ns: "config" } as const;
	if (!u) return i18n.t("relays.pricing.free", T);
	const parts: string[] = [];
	if (u.cost_per_request) parts.push(i18n.t("relays.pricing.perCall", { ...T, price: u.cost_per_request }));
	if (u.cost_per_unit) {
		const price = u.cost_per_unit;
		const bytes = u.units_measure === "body_bytes";
		const chars = u.units_measure === "body_chars" || u.units_measure === "chars";
		const blockSize = u.units_block_size;
		let unitPricing: string;
		if (blockSize && blockSize !== 1) {
			const opts = { ...T, price, count: blockSize };
			unitPricing = bytes
				? i18n.t("relays.pricing.blockByte", opts)
				: chars
					? i18n.t("relays.pricing.blockChar", opts)
					: i18n.t("relays.pricing.blockUnit", opts);
		} else {
			const opts = { ...T, price };
			unitPricing = bytes
				? i18n.t("relays.pricing.perByte", opts)
				: chars
					? i18n.t("relays.pricing.perChar", opts)
					: i18n.t("relays.pricing.perUnit", opts);
		}
		parts.push(u.units_minimum ? i18n.t("relays.pricing.withMinimum", { ...T, pricing: unitPricing, min: u.units_minimum }) : unitPricing);
	}
	if (u.cost_path && !ignoreUpstreamCost) parts.push(i18n.t("relays.pricing.reportedCost", T));
	return parts.length ? parts.join(" + ") : i18n.t("relays.pricing.free", T);
}

/** The mapping a route resolves to: the endpoint's, overridden field by field. */
export function mergeUsage(
	base: RelayUsageConfig | RelayUsageForm | undefined,
	override: RelayUsageConfig | RelayUsageForm | undefined,
): RelayUsageConfig | undefined {
	if (!base && !override) return undefined;
	const out: RelayUsageConfig = {};
	for (const [k, v] of [...Object.entries(base ?? {}), ...Object.entries(override ?? {})]) {
		if (v === undefined || v === null || v === "") continue;
		(out as Record<string, unknown>)[k] = v;
	}
	return out;
}