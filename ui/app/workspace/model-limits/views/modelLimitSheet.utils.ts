// Whether switching providers should drop the model already typed into the sheet.

/**
 * A model survives a provider change when it still exists under the new provider. `*` always
 * survives: it means "every model of the selected provider", so it is provider-agnostic and no
 * catalog lookup can confirm it — searching for it finds nothing and would clear a valid choice.
 */
export function shouldClearModelOnProviderChange(currentModel: string, availableModelNames: string[]): boolean {
	if (!currentModel || currentModel === "*") return false;
	return !availableModelNames.includes(currentModel);
}
/**
 * Whether a limit would be saved with an MCP budget it cannot keep: only a limit for all models on all
 * providers is charged by MCP tool executions. Only the rows that are submitted count, those with an
 * amount, so an empty line left in the editor does not block a narrower limit.
 */
export function hasMCPBudgetOnNarrowLimit(
	modelName: string | undefined,
	provider: string | undefined,
	budgets: { scope?: string; max_limit?: number | null }[] | undefined,
): boolean {
	if (modelName === "*" && !provider) return false;
	return (budgets ?? []).some((b) => b.scope === "mcp" && b.max_limit !== undefined && b.max_limit !== null);
}