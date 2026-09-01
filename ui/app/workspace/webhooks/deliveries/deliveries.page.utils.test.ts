import { describe, expect, it } from "vitest";
import {
	DEFAULT_DELIVERIES_PAGE_SIZE,
	initialIdSearchField,
	normalizeDeliveriesPagination,
	shouldShowEmptyState,
} from "./deliveries.page.utils";

describe("normalizeDeliveriesPagination", () => {
	it("keeps a valid limit and page-aligns the offset", () => {
		expect(normalizeDeliveriesPagination(25, 50)).toEqual({ limit: 25, offset: 50 });
		expect(normalizeDeliveriesPagination(25, 60)).toEqual({ limit: 25, offset: 50 });
	});

	it("clamps a negative offset to the first page", () => {
		expect(normalizeDeliveriesPagination(25, -25)).toEqual({ limit: 25, offset: 0 });
	});

	// Every page-count and page-number calculation divides by the limit, so a
	// zero from "?limit=0" propagates NaN/Infinity through the whole strip.
	it("falls back to the default page size when the limit is zero", () => {
		const { limit, offset } = normalizeDeliveriesPagination(0, 50);
		expect(limit).toBe(DEFAULT_DELIVERIES_PAGE_SIZE);
		expect(Number.isFinite(offset)).toBe(true);
		expect(offset).toBe(50);
	});

	it("falls back to the default page size for negative and non-finite limits", () => {
		expect(normalizeDeliveriesPagination(-10, 0).limit).toBe(DEFAULT_DELIVERIES_PAGE_SIZE);
		expect(normalizeDeliveriesPagination(Number.NaN, 0).limit).toBe(DEFAULT_DELIVERIES_PAGE_SIZE);
		expect(normalizeDeliveriesPagination(Number.POSITIVE_INFINITY, 0).limit).toBe(DEFAULT_DELIVERIES_PAGE_SIZE);
	});

	it("never returns a NaN offset", () => {
		expect(normalizeDeliveriesPagination(0, 0).offset).toBe(0);
	});
});

describe("initialIdSearchField", () => {
	it("defaults to request_id when neither id is set", () => {
		expect(initialIdSearchField({})).toBe("request_id");
	});

	it("stays on request_id when a request id is present", () => {
		expect(initialIdSearchField({ request_id: "req-1" })).toBe("request_id");
	});

	// A shared or reloaded link carrying only delivery_id seeds the input text
	// from that id, so the mode has to match or the first keystroke rewrites it
	// as a request id.
	it("selects delivery_id when only a delivery id is present", () => {
		expect(initialIdSearchField({ delivery_id: "wh-1" })).toBe("delivery_id");
	});

	it("prefers request_id when both are set, matching the seeded text", () => {
		expect(initialIdSearchField({ request_id: "req-1", delivery_id: "wh-1" })).toBe("request_id");
	});
});

describe("shouldShowEmptyState", () => {
	it("shows the empty state for a successful, settled, empty result", () => {
		expect(shouldShowEmptyState({ rowCount: 0, loading: false })).toBe(true);
	});

	it("hides it while loading and when rows are present", () => {
		expect(shouldShowEmptyState({ rowCount: 0, loading: true })).toBe(false);
		expect(shouldShowEmptyState({ rowCount: 3, loading: false })).toBe(false);
	});

	// The page renders its own error alert; the empty row would tell the user to
	// adjust filters that had nothing to do with the failure.
	it("hides it when the query failed", () => {
		expect(shouldShowEmptyState({ rowCount: 0, loading: false, error: { status: 500 } })).toBe(false);
	});
});
