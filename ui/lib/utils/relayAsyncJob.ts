import type { RelayFormValues } from "@/lib/types/schemas";
import { compareRoutes, routeBillableByDefault } from "@/lib/utils/relayForm";

type RouteForm = RelayFormValues["routes"][number];
type JobForm = NonNullable<RouteForm["job"]>;

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

function routeMatches(route: RouteForm, req: Request): boolean {
	if (route.method.toUpperCase() !== req.method) return false;
	if (!pathMatches(route.path.trim(), req.path)) return false;
	// Header values are globs in which "/" is an ordinary character (application/x-amz-json-1.1).
	return route.headers
		.filter((h) => h.name.trim() && h.value.trim())
		.every((h) => {
			const sent = req.headers[h.name.trim().toLowerCase()];
			return sent !== undefined && globMatch(h.value.trim().replaceAll("/", "\u0001"), sent.replaceAll("/", "\u0001"));
		});
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
 * is not billed, "billed" naming the first billed route one reaches, or "unmatched" when none matches.
 */
export function statusCheckTarget(
	routes: RouteForm[],
	job: JobForm,
): { kind: "free"; index: number } | { kind: "billed"; index: number } | { kind: "unmatched" } {
	const order = savedOrder(routes);
	let free = -1;
	for (const id of SAMPLE_JOB_IDS) {
		const req = statusCheck(job, id);
		const index = order.find((i) => routeMatches(routes[i], req));
		if (index === undefined) return { kind: "unmatched" };
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
	const path = job.poll.path.trim().split("?")[0];
	const segments = path.replace(/^\//, "").split("/");
	const at = segments.findIndex((s) => s.includes(JOB_ID_PLACEHOLDER));
	const method = (job.poll.method?.trim() || "POST").toUpperCase() as RouteForm["method"];
	return {
		method,
		path: at < 0 ? path : `/${[...segments.slice(0, at), "**"].join("/")}`,
		headers: Object.entries(job.poll.headers ?? {}).map(([name, value]) => ({ name, value: value.replaceAll(JOB_ID_PLACEHOLDER, "*") })),
		enabled: true,
		...(routeBillableByDefault(method) ? { billable: false } : {}),
	};
}

export type StatusRoutePlan = { kind: "reuse"; index: number } | { kind: "add"; route: RouteForm } | { kind: "blocked"; route: RouteForm };

/**
 * The routes to save for an async job: the start route in place (or appended), plus a free route for
 * the caller's status checks when none takes them yet. An existing billed route is never made free,
 * since it may serve other calls; when one still wins the status check the plan is "blocked".
 */
export function applyAsyncJob(
	routes: RouteForm[],
	editIndex: number,
	start: RouteForm & { job: JobForm },
): { routes: RouteForm[]; plan: StatusRoutePlan } {
	const next = editIndex >= 0 ? routes.map((r, i) => (i === editIndex ? start : r)) : [...routes, start];
	const target = statusCheckTarget(next, start.job);
	if (target.kind === "free") return { routes: next, plan: { kind: "reuse", index: target.index } };
	const route = statusRouteFor(start.job);
	const withStatus = [...next, route];
	const after = statusCheckTarget(withStatus, start.job);
	if (after.kind === "free") return { routes: withStatus, plan: { kind: "add", route } };
	return { routes: next, plan: { kind: "blocked", route: after.kind === "billed" ? withStatus[after.index] : route } };
}

/** For the routes table: each free route that takes a job's status checks, mapped to that job's route. */
export function pairStatusRoutes(routes: RouteForm[]): Map<number, number> {
	const paired = new Map<number, number>();
	routes.forEach((r, i) => {
		if (!r.job) return;
		const target = statusCheckTarget(routes, r.job);
		if (target.kind === "free" && target.index !== i && !routes[target.index].job && !paired.has(target.index)) paired.set(target.index, i);
	});
	return paired;
}