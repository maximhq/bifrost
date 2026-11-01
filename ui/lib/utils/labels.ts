import i18n from "@/lib/i18n";
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
		team: i18n.t("routing.team", { ns: "models" }),
		customer: i18n.t("routing.customer", { ns: "models" }),
		user: i18n.t("routing.user", { ns: "models" }),
	};
	return legacy[scope] || scope;
}