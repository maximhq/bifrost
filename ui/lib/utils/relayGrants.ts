import type { RelayGrant, RelayRouteConfig } from "@/lib/types/relays";

/** Whether a grant's routes mean every route: ["*"], or unset as older grants are. */
export function isAllRoutes(routes: string[] | undefined): boolean {
	return !routes || (routes.length === 1 && routes[0] === "*");
}

/**
 * The selection after a multi-select change, keeping "*" exclusive: picking "All routes" replaces
 * the chosen routes, and picking a route while "All routes" is on drops it.
 */
export function nextRouteSelection(previous: string[], next: string[]): string[] {
	const hadStar = previous.includes("*");
	const hasStar = next.includes("*");
	if (!hadStar && hasStar) return ["*"];
	if (hadStar && hasStar && next.length > 1) return next.filter((id) => id !== "*");
	return next;
}

/** A route as people read it: method, path and any header matchers. */
export function describeRelayRoute(route: Pick<RelayRouteConfig, "method" | "path" | "headers">): string {
	const headers = Object.entries(route.headers ?? {}).map(([name, value]) => `${name}: ${value}`);
	return [`${route.method.toUpperCase()} ${route.path}`, ...headers].join(" · ");
}

/**
 * Grants for a request body: each relay with its routes, "*" only when unset. An empty selection is
 * sent as chosen, never widened to every route; callers block it first (relayGrantWithoutRoutes).
 */
export function relayGrantsPayload(grants: RelayGrant[] | undefined): RelayGrant[] {
	return (grants ?? []).map((g) => ({ relay_name: g.relay_name, routes: g.routes ?? ["*"] }));
}

/** The first relay whose grant selects no route, which would grant nothing; undefined when none. */
export function relayGrantWithoutRoutes(grants: RelayGrant[] | undefined): string | undefined {
	return (grants ?? []).find((g) => g.routes !== undefined && g.routes.length === 0)?.relay_name;
}