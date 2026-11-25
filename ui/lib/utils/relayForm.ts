import i18n from "@/lib/i18n";
import { withBasePath } from "@/lib/utils/basePath";
import type { Relay, RelayJobConfig, RelayRouteConfig, RelaySecret, RelayUsageConfig } from "@/lib/types/relays";
import {
	exactJSONNumbers,
	isJSONText,
	parseJSONExact,
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

/** Escapes text so a glob matches it literally. */
export const escapeGlob = (text: string) => text.replace(/[*?[\]\\]/g, "\\$&");

/** The header matchers a save keeps: named, with a value. */
export const savedHeaders = (headers: { name: string; value: string }[]): Record<string, string> =>
	Object.fromEntries(headers.filter((h) => h.name.trim() && h.value.trim()).map((h) => [h.name.trim(), h.value.trim()]));

/**
 * What identifies a route, since routes have no id: its method, path and header matchers. JSON keeps
 * each part separate, so a header value containing "y: b" cannot pass for a second header.
 */
export function routeKey(r: { method: string; path: string; headers: { name: string; value: string }[] }): string {
	const headers = r.headers
		.filter((h) => h.name.trim())
		.map((h) => [h.name.trim().toLowerCase(), h.value.trim()])
		// Its own comparison, so the key does not depend on the route-ranking helpers.
		.sort(([a, x], [b, y]) => (a < b ? -1 : a > b ? 1 : x < y ? -1 : x > y ? 1 : 0));
	return JSON.stringify([r.method.toUpperCase(), r.path, headers]);
}

/** Where an opened route is now, as the list can refetch in a new order; -1 when it is gone. */
export function locateRoute<T extends Parameters<typeof routeKey>[0]>(routes: T[], index: number, key: string): number {
	if (routes[index] && routeKey(routes[index]) === key) return index;
	return routes.findIndex((r) => routeKey(r) === key);
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
		body_overrides: [],
	};
}

type BodyOverridePair = { name: string; value: string };

/** Forced body fields as the form's path/JSON-text pairs. */
// A whole number past 2^53 that this browser already rounded while reading (no JSON.rawJSON).
function roundedNumber(value: unknown): boolean {
	// Infinity too: a number past a double's range would be saved back as null.
	if (typeof value === "number") return !Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value));
	if (Array.isArray(value)) return value.some(roundedNumber);
	if (value && typeof value === "object") return Object.values(value).some(roundedNumber);
	return false;
}

/** Whether saving this relay from this browser would write back a rounded body override. */
export function hasRoundedBodyOverride(relay: Relay, exact = exactJSONNumbers): boolean {
	if (exact) return false;
	return roundedNumber(relay.body_overrides) || (relay.routes ?? []).some((route) => roundedNumber(route.body_overrides));
}

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
		// Exact: a big whole number keeps its digits on the way to the API.
		out[name] = parseJSONExact(pair.value);
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
		// Saved as listed: the gateway ranks routes itself and reports the order (match_order).
		routes: v.routes.map((r) => {
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

/** The order the gateway tries the routes in, as the API reports it; the saved order when it does not. */
export function displayOrder(matchOrder: number[] | undefined, count: number): number[] {
	const saved = Array.from({ length: count }, (_, i) => i);
	if (!matchOrder || matchOrder.length !== count) return saved;
	return new Set(matchOrder).size === count && matchOrder.every((i) => Number.isInteger(i) && i >= 0 && i < count) ? matchOrder : saved;
}
