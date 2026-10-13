/**
 * A list query that failed with nothing cached should show the load error.
 * Rendering the page's empty state instead would invite creating a first item
 * that may already exist. A failed refetch over cached data keeps the list,
 * and an empty list is a successful response, so the page keeps the empty state.
 */
export function showLoadError(isError: boolean, data: unknown): boolean {
	return isError && data == null;
}