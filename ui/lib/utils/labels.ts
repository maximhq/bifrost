import { getModelLimitScope } from "@/lib/registries/modelLimitScopes";

/**
 * Gets a friendly display name for a scope.
 *
 * Consults the model_limit scope registry first.
 * Falls back to a small built-in map of historical labels,
 * then to the raw scope value.
 */
export function getScopeLabel(scope: string): string {
	const entry = getModelLimitScope(scope);
	if (entry) {
		return entry.label;
	}
	const legacy: Record<string, string> = {
		team: "团队",
		customer: "客户",
		user: "用户",
	};
	return legacy[scope] || scope;
}