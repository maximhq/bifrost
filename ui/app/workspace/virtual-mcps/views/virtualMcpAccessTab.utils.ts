import type { VirtualKey } from "@/lib/types/governance";

/** The team, business unit or customer a virtual key belongs to, as the access tab names it. */
export interface VirtualKeyOwner {
	kind: "team" | "business unit" | "customer";
	name: string;
}

/**
 * Who owns a virtual key, so granting it access to a Virtual MCP can say whose key it is first. A
 * key has at most one owner; its name falls back to the id when the owner was not loaded with it.
 */
export function virtualKeyOwner(
	vk: Pick<VirtualKey, "team_id" | "team" | "business_unit_id" | "business_unit" | "customer_id" | "customer"> | null | undefined,
): VirtualKeyOwner | undefined {
	if (vk?.team_id) return { kind: "team", name: vk.team?.name || vk.team_id };
	if (vk?.business_unit_id) return { kind: "business unit", name: vk.business_unit?.name || vk.business_unit_id };
	if (vk?.customer_id) return { kind: "customer", name: vk.customer?.name || vk.customer_id };
	return undefined;
}