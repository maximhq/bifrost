import { describe, expect, test } from "vitest";
import {
	DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS,
	formatCharacterPriceFull,
	formatCompactNumber,
	formatCurrencyNumber,
	formatTokenPriceCompact,
	formatTokenPriceFull,
} from "./numbers";

describe("formatTokenPriceCompact", () => {
	test("defaults to 6 maximum fraction digits", () => {
		expect(DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS).toBe(6);
	});

	test("formats standard prices with at least 2 decimal places", () => {
		expect(formatTokenPriceCompact(0)).toBe("$0.00");
		expect(formatTokenPriceCompact(0.3 / 1e6)).toBe("$0.30");
		expect(formatTokenPriceCompact(1.25 / 1e6)).toBe("$1.25");
		expect(formatTokenPriceCompact(15 / 1e6)).toBe("$15.00");
		expect(formatTokenPriceCompact(1234.5 / 1e6)).toBe("$1,234.50");
	});

	test("preserves precision for sub-cent rates instead of rounding to 2 decimals", () => {
		// gpt-4o-mini cache read ($0.075 / 1M) was previously rounded to $0.08
		expect(formatTokenPriceCompact(7.5e-8)).toBe("$0.075");

		// ft:gpt-4o-2024-08-06 cache read ($1.875 / 1M) was previously rounded to $1.88
		expect(formatTokenPriceCompact(1.875e-6)).toBe("$1.875");

		// amazon.nova-micro input ($0.035 / 1M) was previously rounded to $0.04
		expect(formatTokenPriceCompact(3.5e-8)).toBe("$0.035");

		// amazon.nova-lite cache read ($0.015 / 1M) was previously rounded to $0.02
		expect(formatTokenPriceCompact(1.5e-8)).toBe("$0.015");

		// claude-3-haiku cache read ($0.025 / 1M) was previously rounded to $0.02
		expect(formatTokenPriceCompact(2.5e-8)).toBe("$0.025");

		// Sub-half-cent rates (< $0.005) previously rounded down to $0.00
		expect(formatTokenPriceCompact(4e-10)).toBe("$0.0004");
		expect(formatTokenPriceCompact(5e-9)).toBe("$0.005");

		// amazon.nova-micro cache read ($0.00875 / 1M) requires 5 decimal places and formats cleanly by default
		expect(formatTokenPriceCompact(8.75e-9)).toBe("$0.00875");
	});

	test("respects custom maximumFractionDigits when provided", () => {
		// Custom 4 decimal places rounds $0.00875 to $0.0088
		expect(formatTokenPriceCompact(8.75e-9, 4)).toBe("$0.0088");
		// Explicit 2 decimal places rounds like fiat currency
		expect(formatTokenPriceCompact(7.5e-8, 2)).toBe("$0.08");
	});

	test("renders dash for undefined, null, and non-finite values", () => {
		expect(formatTokenPriceCompact(undefined)).toBe("—");
		expect(formatTokenPriceCompact(null as unknown as number)).toBe("—");
		expect(formatTokenPriceCompact(Number.NaN)).toBe("—");
		expect(formatTokenPriceCompact(Number.POSITIVE_INFINITY)).toBe("—");
	});
});

describe("formatTokenPriceFull", () => {
	test("formats full token price string with / 1M tokens suffix", () => {
		expect(formatTokenPriceFull(7.5e-8)).toBe("$0.075 / 1M tokens");
		expect(formatTokenPriceFull(1.875e-6)).toBe("$1.875 / 1M tokens");
		expect(formatTokenPriceFull(15 / 1e6)).toBe("$15.00 / 1M tokens");
	});

	test("returns 'Not available' for missing or non-finite values", () => {
		expect(formatTokenPriceFull(undefined)).toBe("Not available");
		expect(formatTokenPriceFull(null as unknown as number)).toBe("Not available");
		expect(formatTokenPriceFull(Number.NaN)).toBe("Not available");
	});
});

describe("formatCharacterPriceFull", () => {
	test("formats full character price string with / 1M characters suffix", () => {
		expect(formatCharacterPriceFull(5e-8)).toBe("$0.05 / 1M characters");
		expect(formatCharacterPriceFull(7.5e-8)).toBe("$0.075 / 1M characters");
	});

	test("returns 'Not available' for missing or non-finite values", () => {
		expect(formatCharacterPriceFull(undefined)).toBe("Not available");
		expect(formatCharacterPriceFull(null as unknown as number)).toBe("Not available");
		expect(formatCharacterPriceFull(Number.NaN)).toBe("Not available");
	});
});

describe("formatCompactNumber", () => {
	test("formats numbers into compact notation", () => {
		expect(formatCompactNumber(1200)).toBe("1.2K");
		expect(formatCompactNumber(1500000)).toBe("1.5M");
		expect(formatCompactNumber(Number.NaN)).toBe("0");
	});
});

describe("formatCurrencyNumber", () => {
	test("formats dollar currency values", () => {
		expect(formatCurrencyNumber(1250)).toBe("$1.25K");
		expect(formatCurrencyNumber(0.005)).toBe("$0.0050");
		expect(formatCurrencyNumber(Number.NaN)).toBe("$0");
	});
});