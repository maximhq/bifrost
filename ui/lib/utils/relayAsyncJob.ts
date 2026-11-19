import type { RelaySecret } from "@/lib/types/relays";
import type { RelayFormValues } from "@/lib/types/schemas";
import { compareRoutes, routeBillableByDefault } from "@/lib/utils/relayForm";
import { isRedacted } from "@/lib/utils/validation";

type RouteForm = RelayFormValues["routes"][number];
type JobForm = NonNullable<RouteForm["job"]>;

/** The relay's extra headers by lower-cased name; null when the browser cannot see the value. */
export type RelayHeaders = Map<string, string | null>;

export function relayHeadersForPlan(extra: Record<string, RelaySecret> | undefined): RelayHeaders {
	return new Map(
		Object.entries(extra ?? {}).map(([name, secret]) => [name.toLowerCase(), secret.ref || isRedacted(secret.value) ? null : secret.value]),
	);
}

export const JOB_ID_PLACEHOLDER = "{{job.id}}";

// The ids the gateway tries a status check with before it saves a job (core/relay/job.go).
const SAMPLE_JOB_IDS = [
	"job-id",
	"0f3c9a1e-7b2d-4c8e-9f10-2a4b6c8d0e1f",
	"9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b",
	"jobs/1",
];

/** Go's path.Match: * and ? stop at "/", [...] is a class, \ escapes. A malformed pattern matches nothing. */
export function globMatch(pattern: string, value: string): boolean {
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
		} else if (pattern[p] === "\\" && p + 1 < pattern.length) result = pattern[p + 1] === value[v] && at(p + 2, v + 1);
		else result = pattern[p] === value[v] && at(p + 1, v + 1);
		memo.set(key, result);
		return result;
	};
	return at(0, 0);
}

function matchClass(pattern: string, start: number, c: string): { matched: boolean; end: number } | null {
	let i = start + 1;
	const negated = pattern[i] === "^";
	if (negated) i++;
	let matched = false;
	let first = true;
	while (i < pattern.length && (first || pattern[i] !== "]")) {
		first = false;
		let lo = pattern[i];
		if (lo === "\\") lo = pattern[++i];
		let hi = lo;
		if (pattern[i + 1] === "-" && i + 2 < pattern.length && pattern[i + 2] !== "]") {
			hi = pattern[i + 2];
			i += 2;
			if (hi === "\\") hi = pattern[++i];
		}
		if (lo <= c && c <= hi) matched = true;
		i++;
	}
	if (i >= pattern.length) return null;
	return { matched: matched !== negated, end: i + 1 };
}

/** A route path glob: segment by segment, with a trailing /** taking any remainder, including none. */
function pathMatches(glob: string, path: string): boolean {
	const segments = path.replace(/^\//, "").split("/");
	let pattern = glob.replace(/^\//, "").split("/");
	const prefix = pattern[pattern.length - 1] === "**";
	if (prefix) pattern = pattern.slice(0, -1);
	if (prefix ? segments.length < pattern.length : segments.length !== pattern.length) return false;
	return pattern.every((p, i) => globMatch(p, segments[i]));
}

interface Request {
	method: string;
	path: string;
	headers: Record<string, string>; // lower-cased names
}

/** Whether the route takes the request; "unknown" when that depends on a relay header the browser cannot read. */
function routeMatches(route: RouteForm, req: Request, relayHeaders: RelayHeaders): boolean | "unknown" {
	if (route.method.toUpperCase() !== req.method) return false;
	if (!pathMatches(route.path.trim(), req.path)) return false;
	let unknown = false;
	for (const h of route.headers) {
		if (!h.name.trim() || !h.value.trim()) continue;
		const name = h.name.trim().toLowerCase();
		// As the gateway does: the request's own header, else the relay's.
		const sent = req.headers[name] ?? relayHeaders.get(name);
		if (sent === undefined) return false;
		if (sent === null) {
			unknown = true;
			continue;
		}
		// Header values are globs in which "/" is an ordinary character (application/x-amz-json-1.1).
		if (!globMatch(h.value.trim().replaceAll("/", "\u0001"), sent.replaceAll("/", "\u0001"))) return false;
	}
	return unknown ? "unknown" : true;
}

/** The status check a caller sends for one job id, as the gateway builds it. */
function statusCheck(job: JobForm, id: string): Request {
	const headers: Record<string, string> = {};
	for (const [name, value] of Object.entries(job.poll.headers ?? {}))
		headers[name.toLowerCase()] = value.replaceAll(JOB_ID_PLACEHOLDER, id);
	if (job.poll.body) headers["content-type"] = job.poll.content_type || "application/json";
	return {
		method: (job.poll.method?.trim() || "POST").toUpperCase(),
		path: job.poll.path.trim().split("?")[0].replaceAll(JOB_ID_PLACEHOLDER, id),
		headers,
	};
}

const headerRecord = (r: RouteForm) =>
	Object.fromEntries(r.headers.filter((h) => h.name.trim() && h.value.trim()).map((h) => [h.name.trim(), h.value.trim()]));

/** Route indices in the order the gateway tries them once saved. */
function savedOrder(routes: RouteForm[]): number[] {
	return routes
		.map((_, i) => i)
		.sort(
			(a, b) =>
				compareRoutes({ ...routes[a], headers: headerRecord(routes[a]) }, { ...routes[b], headers: headerRecord(routes[b]) }) || a - b,
		);
}

export const routeIsBillable = (r: RouteForm) => r.billable ?? routeBillableByDefault(r.method);

/**
 * Where a caller's status checks for this job land: "free" when every sample id reaches a route that
 * is not billed, "disabled" or "billed" naming the first such route one reaches, or "unmatched" when
 * none matches. A disabled route still matches, and the gateway answers 404 instead of trying the next.
 * "unverified" means a secret relay header decides the match, so only the gateway can judge it.
 */
export function statusCheckTarget(
	routes: RouteForm[],
	job: JobForm,
	relayHeaders: RelayHeaders = new Map(),
):
	| { kind: "free"; index: number }
	| { kind: "disabled"; index: number }
	| { kind: "billed"; index: number }
	| { kind: "unmatched" }
	| { kind: "unverified" } {
	const order = savedOrder(routes);
	let free = -1;
	for (const id of SAMPLE_JOB_IDS) {
		const req = statusCheck(job, id);
		const index = order.find((i) => routeMatches(routes[i], req, relayHeaders) !== false);
		if (index === undefined) return { kind: "unmatched" };
		if (routeMatches(routes[index], req, relayHeaders) === "unknown") return { kind: "unverified" };
		if (routes[index].enabled === false) return { kind: "disabled", index };
		if (routeIsBillable(routes[index])) return { kind: "billed", index };
		if (free < 0) free = index;
	}
	return { kind: "free", index: free };
}

/**
 * A free route for the job's status checks. The path keeps everything before the job id and ends in
 * /** there, since an id may hold a slash (an ARN); header values take * in place of the id.
 */
export function statusRouteFor(job: JobForm): RouteForm {
	// The fixed text is sent as is, so a [ or * in it must not act as a glob.
	const headerGlob = (value: string) =>
		value
			.split(JOB_ID_PLACEHOLDER)
			.map((part) => part.replace(/[*?[\\]/g, "\\$&"))
			.join("*");
	const path = job.poll.path.trim().split("?")[0];
	const segments = path.replace(/^\//, "").split("/");
	const at = segments.findIndex((s) => s.includes(JOB_ID_PLACEHOLDER));
	const method = (job.poll.method?.trim() || "POST").toUpperCase() as RouteForm["method"];
	return {
		method,
		path: at < 0 ? path : `/${[...segments.slice(0, at), "**"].join("/")}`,
		headers: Object.entries(job.poll.headers ?? {}).map(([name, value]) => ({ name, value: headerGlob(value) })),
		enabled: true,
		...(routeBillableByDefault(method) ? { billable: false } : {}),
	};
}

export type StatusRoutePlan =
	| { kind: "reuse"; index: number }
	| { kind: "add"; route: RouteForm }
	| { kind: "blocked"; route: RouteForm }
	| { kind: "disabled"; route: RouteForm }
	| { kind: "unverified" }
	| { kind: "duplicateStart"; index: number }
	| { kind: "otherJobCharged"; job: RouteForm; route: RouteForm };

/** Same method, path and header matchers: the gateway only ever reaches the first of two such routes. */
function sameMatcher(a: RouteForm, b: RouteForm): boolean {
	const key = (r: RouteForm) =>
		JSON.stringify([
			r.method.toUpperCase(),
			r.path.trim(),
			r.headers
				.filter((h) => h.name.trim() && h.value.trim())
				.map((h) => `${h.name.trim().toLowerCase()}:${h.value.trim()}`)
				.sort(),
		]);
	return key(a) === key(b);
}

/**
 * The routes to save for an async job: the start route in place (or appended), plus a free route for
 * the caller's status checks when none takes them yet. An existing billed route is never made free,
 * since it may serve other calls; when one still wins the status check the plan is "blocked", and a
 * disabled route that wins it makes the plan "disabled".
 */
export function applyAsyncJob(
	routes: RouteForm[],
	editIndex: number,
	start: RouteForm & { job: JobForm },
	relayHeaders: RelayHeaders = new Map(),
): { routes: RouteForm[]; plan: StatusRoutePlan } {
	const result = planAsyncJob(routes, editIndex, start, relayHeaders);
	if (result.plan.kind === "blocked" || result.plan.kind === "disabled" || result.plan.kind === "duplicateStart") return result;
	// The gateway checks every job on save, and this save can move another job's status checks.
	const startIndex = editIndex >= 0 ? editIndex : routes.length;
	for (const [i, r] of result.routes.entries()) {
		if (!r.job || i === startIndex) continue;
		const target = statusCheckTarget(result.routes, r.job, relayHeaders);
		if (target.kind === "billed") return { routes: routes, plan: { kind: "otherJobCharged", job: r, route: result.routes[target.index] } };
	}
	return result;
}

function planAsyncJob(
	routes: RouteForm[],
	editIndex: number,
	start: RouteForm & { job: JobForm },
	relayHeaders: RelayHeaders,
): { routes: RouteForm[]; plan: StatusRoutePlan } {
	const next = editIndex >= 0 ? routes.map((r, i) => (i === editIndex ? start : r)) : [...routes, start];
	const startIndex = editIndex >= 0 ? editIndex : next.length - 1;
	const duplicate = next.findIndex((r, i) => i !== startIndex && sameMatcher(r, start));
	if (duplicate >= 0) return { routes: next, plan: { kind: "duplicateStart", index: duplicate } };
	const target = statusCheckTarget(next, start.job, relayHeaders);
	if (target.kind === "free") return { routes: next, plan: { kind: "reuse", index: target.index } };
	if (target.kind === "unverified") return { routes: next, plan: { kind: "unverified" } };
	const route = statusRouteFor(start.job);
	const withStatus = [...next, route];
	const after = statusCheckTarget(withStatus, start.job, relayHeaders);
	if (after.kind === "free") return { routes: withStatus, plan: { kind: "add", route } };
	if (after.kind === "unverified") return { routes: next, plan: { kind: "unverified" } };
	if (after.kind === "disabled") return { routes: next, plan: { kind: "disabled", route: withStatus[after.index] } };
	return { routes: next, plan: { kind: "blocked", route: after.kind === "billed" ? withStatus[after.index] : route } };
}

/** For the routes table: each free route that takes a job's status checks, mapped to that job's route. */
export function pairStatusRoutes(routes: RouteForm[], relayHeaders: RelayHeaders = new Map()): Map<number, number> {
	const paired = new Map<number, number>();
	routes.forEach((r, i) => {
		if (!r.job) return;
		const target = statusCheckTarget(routes, r.job, relayHeaders);
		if (target.kind === "free" && target.index !== i && !routes[target.index].job && !paired.has(target.index)) paired.set(target.index, i);
	});
	return paired;
}