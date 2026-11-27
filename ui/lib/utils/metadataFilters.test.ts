import { describe, expect, it } from "vitest";
import {
	metadataFilterQueryValue,
	metadataFilterValueCount,
	normalizeMetadataFilters,
	parseMetadataFiltersParam,
	toggleMetadataFilterValue,
} from "./metadataFilters";

describe("parseMetadataFiltersParam", () => {
	it("reads a link written before multiple selection, one string per key", () => {
		expect(parseMetadataFiltersParam('{"env":"prod","app":"web"}')).toEqual({ env: ["prod"], app: ["web"] });
	});

	it("reads several values for a key", () => {
		expect(parseMetadataFiltersParam('{"env":["prod","staging"],"app":"web"}')).toEqual({ env: ["prod", "staging"], app: ["web"] });
	});

	it("drops empty values, duplicates and keys left with nothing", () => {
		expect(parseMetadataFiltersParam('{"env":["prod","","prod"],"app":[],"tier":""}')).toEqual({ env: ["prod"] });
	});

	it("returns undefined for an empty, malformed or non-object param", () => {
		expect(parseMetadataFiltersParam("")).toBeUndefined();
		expect(parseMetadataFiltersParam("{nope")).toBeUndefined();
		expect(parseMetadataFiltersParam('["prod"]')).toBeUndefined();
		expect(parseMetadataFiltersParam('{"env":[]}')).toBeUndefined();
	});
});

describe("normalizeMetadataFilters", () => {
	it("ignores values that are not strings", () => {
		expect(normalizeMetadataFilters({ env: ["prod", 3, null], n: 4 })).toEqual({ env: ["prod"] });
	});
});

describe("toggleMetadataFilterValue", () => {
	it("adds a second value for the same key instead of replacing the first", () => {
		expect(toggleMetadataFilterValue({ env: ["prod"] }, "env", "staging")).toEqual({ env: ["prod", "staging"] });
	});

	it("removes a selected value and leaves the others", () => {
		expect(toggleMetadataFilterValue({ env: ["prod", "staging"], app: ["web"] }, "env", "prod")).toEqual({
			env: ["staging"],
			app: ["web"],
		});
	});

	it("drops the key when its last value is removed, and the filter when no key is left", () => {
		expect(toggleMetadataFilterValue({ env: ["prod"], app: ["web"] }, "env", "prod")).toEqual({ app: ["web"] });
		expect(toggleMetadataFilterValue({ env: ["prod"] }, "env", "prod")).toBeUndefined();
	});

	it("adds to a filter still holding one string per key", () => {
		const legacy = { env: "prod" } as unknown as Record<string, string[]>;
		expect(toggleMetadataFilterValue(legacy, "env", "staging")).toEqual({ env: ["prod", "staging"] });
	});

	it("starts a filter from nothing", () => {
		expect(toggleMetadataFilterValue(undefined, "env", "prod")).toEqual({ env: ["prod"] });
	});
});

describe("metadataFilterQueryValue", () => {
	it("sends one value as itself, commas included", () => {
		expect(metadataFilterQueryValue(["prod"])).toBe("prod");
		expect(metadataFilterQueryValue(["a,b"])).toBe("a,b");
	});

	it("sends several values as a JSON array", () => {
		expect(metadataFilterQueryValue(["prod", "staging"])).toBe('["prod","staging"]');
	});

	it("wraps one value that would read as a JSON array so it stays literal", () => {
		expect(metadataFilterQueryValue(['["x"]'])).toBe('["[\\"x\\"]"]');
	});
});

describe("metadataFilterValueCount", () => {
	it("counts every selected value across keys", () => {
		expect(metadataFilterValueCount({ env: ["prod", "staging"], app: ["web"] })).toBe(3);
		expect(metadataFilterValueCount(undefined)).toBe(0);
	});
});