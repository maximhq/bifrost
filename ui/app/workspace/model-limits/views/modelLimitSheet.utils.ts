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