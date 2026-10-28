import type { Relay, RelayJobConfig, RelayRouteConfig, RelaySecret, RelayUsageConfig } from "@/lib/types/relays";
import {
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

const GLOB_CHARS = /[*?[]/;
const codePointOrder = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

/** The sort key of a route path; see compareRoutes. */
function pathRank(path: string): number[] {
	const segments = path.replace(/^\//, "").split("/");
	const catchAll = segments[segments.length - 1] === "**";
	const fixed = catchAll ? segments.slice(0, -1) : segments;
	const glob = fixed.some((s) => GLOB_CHARS.test(s));
	// Equal literal text, fewer and narrower wildcards first: /x/foo* is covered by /x/*foo*, and
	// /files/[m] by /files/[a-z] by /files/? by /files/*.
	const shape = globShape(fixed.join("/"));
	return [
		catchAll ? 1 : 0,
		catchAll ? -fixed.length : 0,
		glob ? 1 : 0,
		glob ? fixed.length : 0,
		glob ? -shape.literal : 0,
		glob ? shape.stars : 0,
		glob ? shape.singles : 0,
	];
}

// How many characters a ? (or a negated class) can stand for, for ranking only.
const ANY_CHARACTER = 0x110000;

/**
 * The shape of a path.Match pattern, as the gateway reads it: literal characters (outside brackets,
 * an escaped character included), * wildcards, and the breadth of the single-character positions: a
 * class counts the characters it matches ([m] 1, [a-z] 26, a negated one nearly all), and a ? all.
 */
function globShape(pattern: string): { literal: number; stars: number; singles: number } {
	let literal = 0;
	let stars = 0;
	let singles = 0;
	for (let i = 0; i < pattern.length; i++) {
		const c = pattern[i];
		if (c === "\\" && i + 1 < pattern.length) {
			literal++;
			i++;
		} else if (c === "*") stars++;
		else if (c === "?") singles += ANY_CHARACTER;
		else if (c === "[") {
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
	return { literal, stars, singles };
}

/**
 * The order routes are saved in. The relay takes the first route that matches, so a route must come
 * before every broader route that also matches its requests. Literal paths never overlap one another
 * and come first, A to Z. Globs follow by depth, more literal text first (/models/*:predict before
 * /models/*), and a trailing /** catch-all comes last, deepest first. On the same path a route with
 * header matchers precedes the plain one, and exact header values precede globbed ones, since the
 * broader route would otherwise take their requests.
 */
export function compareRoutes(a: RouteOrderKey, b: RouteOrderKey): number {
	const ra = pathRank(a.path);
	const rb = pathRank(b.path);
	for (let i = 0; i < ra.length; i++) if (ra[i] !== rb[i]) return ra[i] - rb[i];
	const headerKey = (r: RouteOrderKey) =>
		Object.entries(r.headers ?? {})
			.map(([name, value]) => `${name.toLowerCase()}: ${value}`)
			.sort()
			.join("\n");
	// Header values are globs too: on one path, X-Amz-Target: * must not precede an exact target.
	const ha = headerRank(a);
	const hb = headerRank(b);
	return (
		codePointOrder(a.path, b.path) ||
		Object.keys(b.headers ?? {}).length - Object.keys(a.headers ?? {}).length ||
		ha[0] - hb[0] ||
		ha[1] - hb[1] ||
		ha[2] - hb[2] ||
		ha[3] - hb[3] ||
		codePointOrder(a.method.toUpperCase(), b.method.toUpperCase()) ||
		codePointOrder(headerKey(a), headerKey(b))
	);
}

/**
 * How broad a route's header matchers are: fewer glob values first, then, among globs, more literal
 * text first (Textract.Analyze* before *), then fewer and narrower wildcards (foo* before *foo*,
 * v[12] before v[0-9] before v?). Exact values never overlap, so they stay A to Z.
 */
function headerRank(r: RouteOrderKey): [number, number, number, number] {
	const values = Object.values(r.headers ?? {});
	const globs = values.filter((v) => GLOB_CHARS.test(v)).length;
	const shapes = values.map(globShape);
	const sum = (k: "literal" | "stars" | "singles") => shapes.reduce((n, shape) => n + shape[k], 0);
	return [globs, globs ? -sum("literal") : 0, globs ? sum("stars") : 0, globs ? sum("singles") : 0];
}

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
	};
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
		})),
		skip_route_check: e.skip_route_check ?? false,
		ignore_upstream_cost: e.ignore_upstream_cost ?? false,
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
		proxy_config:
			v.proxy.type === "inherit"
				? undefined
				: v.proxy.type === "none"
					? { type: "none" }
					: {
							type: v.proxy.type,
							url: toApiSecret(v.proxy.url),
							username: toApiSecret(v.proxy.username),
							password: toApiSecret(v.proxy.password),
							ca_cert_pem: toApiSecret(v.proxy.ca_cert_pem),
						},
		usage: cleanUsage(v.usage, false),
		streaming: streamingSet ? { ...v.streaming, paths: v.streaming.paths.filter((p) => p.trim()) } : undefined,
		routes: v.routes
			.map((r) => {
				const headers = Object.fromEntries(
					r.headers.filter((h) => h.name.trim() && h.value.trim()).map((h) => [h.name.trim(), h.value.trim()]),
				);
				// A route that is not billed is never priced and hides its pricing, so only the model and
				// token mapping the gateway still reads on every call is sent.
				const usage =
					(r.billable ?? routeBillableByDefault(r.method)) ? cleanUsage(r.usage, true) : withoutPricing(cleanUsage(r.usage, true));
				return {
					method: r.method,
					path: r.path.trim(),
					...(Object.keys(headers).length ? { headers } : {}),
					...(r.enabled === false ? { enabled: false } : {}),
					...(r.billable !== undefined && r.billable !== routeBillableByDefault(r.method) ? { billable: r.billable } : {}),
					...(r.stream !== undefined ? { stream: r.stream } : {}),
					...(usage ? { usage } : {}),
					...(r.job ? { job: cleanJob(r.job) } : {}),
				};
			})
			.sort(compareRoutes),
		skip_route_check: v.skip_route_check || undefined,
		ignore_upstream_cost: v.ignore_upstream_cost || undefined,
	};
}

/** Drops a job's empty optional fields so the stored config carries only what was set. */
export function cleanJob(job: Omit<RelayJobConfig, "usage"> & { usage?: RelayUsageForm }): RelayJobConfig {
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
	};
}

/** A one-line summary of what a call costs under a mapping, for the route table and pricing tab. */
// ignoreUpstreamCost drops the reported cost, as the server does when the relay ignores it.
export function describePricing(u: RelayUsageConfig | undefined, ignoreUpstreamCost = false): string {
	if (!u) return "Free";
	const parts: string[] = [];
	if (u.cost_per_request) parts.push(`$${u.cost_per_request} / call`);
	if (u.cost_per_unit) {
		const what =
			u.units_measure === "body_bytes" ? "byte" : u.units_measure === "body_chars" || u.units_measure === "chars" ? "char" : "unit";
		const block = u.units_block_size && u.units_block_size !== 1 ? ` per ${u.units_block_size} ${what}s` : ` / ${what}`;
		parts.push(`$${u.cost_per_unit}${block}${u.units_minimum ? `, min ${u.units_minimum}` : ""}`);
	}
	if (u.cost_path && !ignoreUpstreamCost) parts.push(`+ reported cost`);
	return parts.length ? parts.join(" + ") : "Free";
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