import type { WebhookDeliveryFilters } from "@/lib/types/webhooks";

/** Page size used when the URL carries no usable `limit`. */
export const DEFAULT_DELIVERIES_PAGE_SIZE = 25;

export interface NormalizedDeliveriesPagination {
	limit: number;
	offset: number;
}

/**
 * Both `limit` and `offset` come from hand-editable URL parameters, and every
 * page-count, page-number and "x-y of n" calculation divides by the limit. A
 * zero, negative or non-finite limit therefore turns the whole pagination
 * strip into NaN/Infinity and sends an invalid query — with no way back except
 * editing the URL again. Clamp the limit to a usable page size first, then
 * align the offset to a page boundary of that size.
 */
export function normalizeDeliveriesPagination(limit: number, offset: number): NormalizedDeliveriesPagination {
	const safeLimit = Number.isFinite(limit) && limit >= 1 ? Math.floor(limit) : DEFAULT_DELIVERIES_PAGE_SIZE;
	return { limit: safeLimit, offset: Math.max(0, Math.floor(offset / safeLimit) * safeLimit) };
}

/**
 * The id search box looks up either a request id or a delivery id, and its
 * text is seeded from whichever the filters carry. The mode has to be seeded
 * from the same place: a link carrying only `delivery_id` would otherwise show
 * that id under a "request ID" placeholder, and the first keystroke would
 * write it back as a request id.
 */
export function initialIdSearchField(filters: WebhookDeliveryFilters): "request_id" | "delivery_id" {
	return !filters.request_id && filters.delivery_id ? "delivery_id" : "request_id";
}

/**
 * The empty-state row tells the user to adjust their filters, which is wrong
 * advice when the query failed — the page already shows the error alert, and
 * the empty row would blame the filters for it.
 */
export function shouldShowEmptyState({ rowCount, loading, error }: { rowCount: number; loading: boolean; error?: unknown }): boolean {
	return rowCount === 0 && !loading && !error;
}
