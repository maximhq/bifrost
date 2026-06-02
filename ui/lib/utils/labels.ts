/**
 * Gets a friendly display name for a scope
 * @param scope - The scope value (global|team|customer|virtual_key)
 * @returns Friendly display name
 */
export function getScopeLabel(scope: string): string {
	const labels: Record<string, string> = {
		global: "Global",
		team: "Team",
		customer: "Customer",
		virtual_key: "Virtual Key",
	};
	return labels[scope] || scope;
}