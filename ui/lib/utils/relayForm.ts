import type { Relay, RelayJobConfig, RelayRouteConfig, RelaySecret, RelayUsageConfig } from "@/lib/types/relays";
import { type RelayFormValues, relayAuthSchema, relayNetworkFormSchema, relayUsageBaseSchema } from "@/lib/types/schemas";

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

/** A route is billable by default only for POST, so the flag is stored only when it differs. */
export function routeBillableByDefault(method: string): boolean {
	return method.toUpperCase() === "POST";
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
		routes: v.routes.map((r) => {
			const headers = Object.fromEntries(
				r.headers.filter((h) => h.name.trim() && h.value.trim()).map((h) => [h.name.trim(), h.value.trim()]),
			);
			return {
				method: r.method,
				path: r.path.trim(),
				...(Object.keys(headers).length ? { headers } : {}),
				...(r.enabled === false ? { enabled: false } : {}),
				...(r.billable !== undefined && r.billable !== routeBillableByDefault(r.method) ? { billable: r.billable } : {}),
				...(r.stream !== undefined ? { stream: r.stream } : {}),
				...(cleanUsage(r.usage, true) ? { usage: cleanUsage(r.usage, true) } : {}),
				...(r.job ? { job: cleanJob(r.job) } : {}),
			};
		}),
		skip_route_check: v.skip_route_check || undefined,
		ignore_upstream_cost: v.ignore_upstream_cost || undefined,
	};
}

/** Drops a job's empty optional fields so the stored config carries only what was set. */
export function cleanJob(job: RelayJobConfig): RelayJobConfig {
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
export function mergeUsage(base: RelayUsageConfig | undefined, override: RelayUsageConfig | undefined): RelayUsageConfig | undefined {
	if (!base && !override) return undefined;
	const out: RelayUsageConfig = { ...(base ?? {}) };
	for (const [k, v] of Object.entries(override ?? {})) {
		if (v === undefined || v === "") continue;
		(out as Record<string, unknown>)[k] = v;
	}
	return out;
}