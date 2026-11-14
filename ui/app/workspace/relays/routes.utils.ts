import type { Relay } from "@/lib/types/relays";
import type { RelayFormValues } from "@/lib/types/schemas";
import { toRelayConfig, toRelayForm } from "@/lib/utils/relayForm";

export type RouteForm = RelayFormValues["routes"][number];

/** The endpoint body to save with its routes replaced; secrets go back redacted and are kept by the server. */
export function withRoutes(endpoint: Relay, routes: RouteForm[]): Relay {
	const form = toRelayForm(endpoint);
	return toRelayConfig({ ...form, routes });
}