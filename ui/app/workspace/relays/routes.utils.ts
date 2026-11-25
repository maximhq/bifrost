import i18n from "@/lib/i18n";
import type { Relay } from "@/lib/types/relays";
import type { RelayRouteForm } from "@/lib/types/schemas";
import { hasRoundedBodyOverride, routeKey, toRelayConfig, toRelayForm } from "@/lib/utils/relayForm";

export type RouteForm = RelayRouteForm;

/** The endpoint body to save with its routes replaced; secrets go back redacted and are kept by the server. */
export function withRoutes(endpoint: Relay, routes: RouteForm[]): Relay {
	// Method, path and headers are what tell routes apart, and a repeat could never match.
	const keys = routes.map(routeKey);
	if (new Set(keys).size !== keys.length) throw new Error(i18n.t("validation.relays.routeRepeated", { ns: "common" }));
	const next = toRelayConfig({ ...toRelayForm(endpoint), routes });
	// A route change re-sends every override it keeps, so a number this browser rounded would be
	// overwritten; deleting the route that holds it is fine, since nothing is written back.
	if (hasRoundedBodyOverride(next)) throw new Error(i18n.t("validation.relays.bodyOverrideRounded", { ns: "common" }));
	return next;
}