/**
 * A list query that failed with nothing cached should show the load error.
 * Rendering the page's empty state instead would invite creating a first item
 * that may already exist. A failed refetch over cached data keeps the list,
 * and an empty list is a successful response, so the page keeps the empty state.
 *
 * Takes the query's error, not isError: while a refetch after a failure is in
 * flight (polling or Retry), RTK Query reports isError false but keeps error
 * until a request succeeds, so the page stays on the load error.
 */
export function showLoadError(error: unknown, data: unknown): boolean {
	return error != null && data == null;
}