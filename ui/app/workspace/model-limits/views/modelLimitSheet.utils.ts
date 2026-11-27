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
 * Whether a save has nothing to send: no line on the form with an amount, nothing kept for the other
 * page, and, on the MCP page, no existing limit being emptied (that deletes it). Lines the other page
 * owns (a holder-level row's lines of the scope this page hides, saved back unchanged) only keep an
 * edit alive: an Add that enters nothing of its own would report a limit created that was never added.
 */
export function saveHasNoLimit(hasAnyLimit: boolean, isEditing: boolean, keepsOtherPagesLines: boolean, deletesOnEmpty: boolean): boolean {
	return !hasAnyLimit && !(isEditing && keepsOtherPagesLines) && !deletesOnEmpty;
}