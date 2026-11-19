import type { RelayFormValues } from "@/lib/types/schemas";

type RouteForm = RelayFormValues["routes"][number];
type JobForm = NonNullable<RouteForm["job"]>;

export const JOB_ID_PLACEHOLDER = "{{job.id}}";

/** Same method, path and header matchers: the gateway only ever reaches the first of two such routes. */
function sameMatcher(a: RouteForm, b: RouteForm): boolean {
	return matcherKey(a) === matcherKey(b);
}

// Keyed on what a save keeps, so a matcher with a blank value does not tell two routes apart. JSON,
// not routeKey: a header value may hold spaces and colons, which routeKey joins its parts with.
const matcherKey = (r: RouteForm) =>
	JSON.stringify([
		r.method.toUpperCase(),
		r.path.trim(),
		r.headers
			.filter((h) => h.name.trim() && h.value.trim())
			.map((h) => `${h.name.trim().toLowerCase()}:${h.value.trim()}`)
			.sort(),
	]);

/**
 * The routes to save for an async job: the start route in place, or appended. A start route with the
 * same matchers as another route is refused, since the gateway would only ever reach the first. The
 * caller's own status checks need no route of their own: the gateway relays them free.
 */
export function applyAsyncJob(
	routes: RouteForm[],
	editIndex: number,
	start: RouteForm & { job: JobForm },
): { routes: RouteForm[]; duplicateOf?: number } {
	const next = editIndex >= 0 ? routes.map((r, i) => (i === editIndex ? start : r)) : [...routes, start];
	const startIndex = editIndex >= 0 ? editIndex : next.length - 1;
	const duplicate = next.findIndex((r, i) => i !== startIndex && sameMatcher(r, start));
	return duplicate >= 0 ? { routes: next, duplicateOf: duplicate } : { routes: next };
}

const setValue = <T>(v: T | null | undefined | "") => (v === undefined || v === null || v === "" ? undefined : v);

/** Whether the job's pricing, merged over the route's and the relay's as the gateway does, bills request-side units. */
export function jobBillsRequestUnits(
	...layers: ({ units_source?: string | null; cost_per_unit?: number | null } | null | undefined)[]
): boolean {
	let source: string | undefined;
	let rate: number | undefined;
	for (const u of layers) {
		source = setValue(u?.units_source) ?? source;
		rate = setValue(u?.cost_per_unit) ?? rate;
	}
	return source === "request" && rate !== undefined && rate > 0;
}