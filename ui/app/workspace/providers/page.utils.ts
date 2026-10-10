/**
 * A failed providers query with nothing cached should show the load error.
 * An empty list is a successful response, so the page keeps the empty state.
 */
export function showProvidersLoadError(isError: boolean, providers: unknown): boolean {
	return isError && providers == null;
}
