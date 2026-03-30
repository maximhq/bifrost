import { RoutingRule } from "@/lib/types/routingRules";

export const POSITIONS_COOKIE = "bf-routing-tree-positions";

export interface PositionCookie {
	fingerprint: string;
	positions: Record<string, { x: number; y: number }>;
	viewport?: { x: number; y: number; zoom: number };
}

/** Changes whenever any rule is added, edited, or deleted. */
export function computeFingerprint(rules: RoutingRule[]): string {
	return rules
		.map((r) => `${r.id}:${r.updated_at}`)
		.sort()
		.join("|");
}
