import { JOB_ID_PLACEHOLDER, type RelaySecret } from "@/lib/types/relays";
import type { RelayRouteForm as RouteForm } from "@/lib/types/schemas";
import {
	escapeGlob,
	globMatch,
	headerGlobMatches,
	matchOrder,
	routeBillableByDefault,
	saveOrder,
	savedHeaders,
} from "@/lib/utils/relayForm";
import { isRedacted } from "@/lib/utils/validation";

type JobForm = NonNullable<RouteForm["job"]>;

/** The relay's extra headers by lower-cased name; null when the browser cannot see the value. */
export type RelayHeaders = Map<string, string | null>;

export function relayHeadersForPlan(extra: Record<string, RelaySecret> | undefined): RelayHeaders {
	return new Map(
		Object.entries(extra ?? {}).map(([name, secret]) => [name.toLowerCase(), secret.ref || isRedacted(secret.value) ? null : secret.value]),
	);
}

// The ids the gateway tries a status check with before it saves a job (jobStatusSampleIDs in core/relay/job.go).
export const SAMPLE_JOB_IDS = [
	"job-id",
	"0f3c9a1e-7b2d-4c8e-9f10-2a4b6c8d0e1f",
	"9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b",
	"jobs/1",
	"a/b/c",
];

// The most segments a middle ** takes (maxGapSegments in core/relay/routes.go).
export const MAX_GAP_SEGMENTS = 80;

const gapSegmentOK = (segment: string) => {
	const name = segment.split(";")[0];
	return name !== "" && name !== "." && name !== "..";
};

/** A route path glob: segment by segment. A trailing /** takes any remainder, a middle one one or more segments. */
function pathMatches(glob: string, path: string): boolean {
	const segments = path.replace(/^\//, "").split("/");
	let pattern = glob.replace(/^\//, "").split("/");
	const prefix = pattern[pattern.length - 1] === "**";
	if (prefix) pattern = pattern.slice(0, -1);
	const at = pattern.indexOf("**");
	if (at >= 0) {
		const head = pattern.slice(0, at);
		const tail = pattern.slice(at + 1);
		const gap = segments.length - head.length - tail.length;
		if (gap < 1 || gap > MAX_GAP_SEGMENTS) return false;
		return (
			head.every((p, i) => globMatch(p, segments[i])) &&
			tail.every((p, i) => globMatch(p, segments[at + gap + i])) &&
			segments.slice(at, at + gap).every(gapSegmentOK)
		);
	}
	if (prefix ? segments.length < pattern.length : segments.length !== pattern.length) return false;
	// A trailing ** refuses dot segments as a middle one does; an empty one is a trailing slash.
	if (prefix && segments.slice(pattern.length).some((s) => s !== "" && !gapSegmentOK(s))) return false;
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
		if (!headerGlobMatches(h.value.trim(), sent)) return false;
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

const orderKeys = (routes: RouteForm[]) => routes.map((r) => ({ ...r, headers: savedHeaders(r.headers) }));

/** Route indices in the order a save writes the form's routes. */
const formSaveOrder = (routes: RouteForm[]) => saveOrder(orderKeys(routes));

/** Route indices in the order the gateway tries the stored routes. */
export const liveOrder = (routes: RouteForm[]) => matchOrder(orderKeys(routes));

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
	order: number[] = formSaveOrder(routes),
):
	| { kind: "free"; index: number }
	| { kind: "disabled"; index: number }
	| { kind: "billed"; index: number }
	| { kind: "unmatched" }
	| { kind: "unverified" } {
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

/** A charged or disabled catch-all deeper than the ** of a generated route, which that route would sort ahead of. */
function crossedCatchAll(routes: RouteForm[], status: RouteForm): RouteForm | undefined {
	const segments = status.path.replace(/^\//, "").split("/");
	const at = segments.indexOf("**");
	if (at < 0 || at === segments.length - 1) return undefined;
	const head = segments.slice(0, at);
	return routes.find((r) => {
		if (r.method.toUpperCase() !== status.method || (r.enabled !== false && !routeIsBillable(r))) return false;
		const path = r.path.trim().replace(/^\//, "").split("/");
		return path[path.length - 1] === "**" && path.length - 1 > head.length && head.every((h, i) => globMatch(path[i], h));
	});
}

/**
 * A free route for the job's status checks. A ** takes the job id, since an id may hold a slash (an
 * ARN), and the text after the id stays in the path so other calls keep their price. Header values
 * take * for the id.
 */
export function statusRouteFor(job: JobForm): RouteForm {
	const headerGlob = (value: string) => value.split(JOB_ID_PLACEHOLDER).map(escapeGlob).join("*");
	const path = job.poll.path.trim().split("?")[0];
	const segments = path.replace(/^\//, "").split("/");
	const at = segments.findIndex((s) => s.includes(JOB_ID_PLACEHOLDER));
	const method = (job.poll.method?.trim() || "POST").toUpperCase() as RouteForm["method"];
	return {
		method,
		path: at < 0 ? escapeGlob(path) : statusPath(segments, at),
		headers: Object.entries(job.poll.headers ?? {}).map(([name, value]) => ({ name, value: headerGlob(value) })),
		enabled: true,
		...(routeBillableByDefault(method) ? { billable: false } : {}),
	};
}

function statusPath(segments: string[], at: number): string {
	const head = segments.slice(0, at);
	const tail = segments.slice(at + 1);
	const narrow = tail.length > 0 && !tail.some((s) => s.includes(JOB_ID_PLACEHOLDER));
	return `/${[...head.map(escapeGlob), "**", ...(narrow ? tail.map(escapeGlob) : [])].join("/")}`;
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
	// JSON, not routeKey: a header value may hold spaces and colons, which routeKey joins its parts with.
	const key = (r: RouteForm) =>
		JSON.stringify([
			r.method.toUpperCase(),
			r.path.trim(),
			Object.entries(savedHeaders(r.headers))
				.map(([name, value]) => [name.toLowerCase(), value])
				.sort(([x], [y]) => (x < y ? -1 : x > y ? 1 : 0)),
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
	// Adding it would make part of that route's calls free, so the person has to decide.
	const crossed = crossedCatchAll(next, route);
	if (crossed) return { routes: next, plan: { kind: crossed.enabled === false ? "disabled" : "blocked", route: crossed } };
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
	const order = liveOrder(routes);
	routes.forEach((r, i) => {
		if (!r.job) return;
		const target = statusCheckTarget(routes, r.job, relayHeaders, order);
		if (target.kind === "free" && target.index !== i && !routes[target.index].job && !paired.has(target.index)) paired.set(target.index, i);
	});
	return paired;
}