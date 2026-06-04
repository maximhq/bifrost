import { describe, test, expect } from "vitest";
import { cleanNumericInput } from "./strings";

// Simulate what onChange does: clean → Number()
function simulateOnChange(raw: string): { display: string; value: number | undefined } {
	const cleaned = cleanNumericInput(raw);
	if (cleaned === "" || cleaned === ".") {
		return { display: cleaned, value: undefined };
	}
	const n = Number(cleaned);
	return { display: cleaned, value: isNaN(n) ? undefined : n };
}

// Simulate what onBlur does: normalize display string
function simulateOnBlur(displayValue: string): { display: string; value: number | undefined } {
	const trimmed = displayValue.trim();
	if (trimmed === "" || trimmed === ".") {
		return { display: "", value: undefined };
	}
	const num = Number(trimmed);
	if (!isNaN(num)) {
		return { display: String(num), value: num };
	}
	return { display: "", value: undefined };
}

describe("cleanNumericInput", () => {
	// Basic valid inputs
	test("empty string", () => expect(cleanNumericInput("")).toBe(""));
	test("single digit", () => expect(cleanNumericInput("5")).toBe("5"));
	test("multiple digits", () => expect(cleanNumericInput("123")).toBe("123"));
	test("decimal number", () => expect(cleanNumericInput("1.5")).toBe("1.5"));
	test("leading decimal", () => expect(cleanNumericInput(".5")).toBe(".5"));
	test("trailing decimal", () => expect(cleanNumericInput("5.")).toBe("5."));
	test("zero", () => expect(cleanNumericInput("0")).toBe("0"));
	test("decimal zero", () => expect(cleanNumericInput("0.0")).toBe("0.0"));
	test("long decimal", () => expect(cleanNumericInput("123.456")).toBe("123.456"));

	// Comma-separated (thousands)
	test("1,000", () => expect(cleanNumericInput("1,000")).toBe("1000"));
	test("1,000,000", () => expect(cleanNumericInput("1,000,000")).toBe("1000000"));
	test("1,234.56", () => expect(cleanNumericInput("1,234.56")).toBe("1234.56"));

	// Underscore-separated (programming style)
	test("1_000", () => expect(cleanNumericInput("1_000")).toBe("1000"));
	test("1_000_000", () => expect(cleanNumericInput("1_000_000")).toBe("1000000"));

	// Space-separated
	test("1 000", () => expect(cleanNumericInput("1 000")).toBe("1000"));
	test("1 000 000", () => expect(cleanNumericInput("1 000 000")).toBe("1000000"));

	// Alphabetic characters — should stop
	test("1abc", () => expect(cleanNumericInput("1abc")).toBe("1"));
	test("123abc456", () => expect(cleanNumericInput("123abc456")).toBe("123"));
	test("abc123", () => expect(cleanNumericInput("abc123")).toBe(""));

	// Multiple consecutive separators — should stop
	test("1,,000", () => expect(cleanNumericInput("1,,000")).toBe("1"));
	test("1..5", () => expect(cleanNumericInput("1..5")).toBe("1.5"));
	test("1  000", () => expect(cleanNumericInput("1  000")).toBe("1"));

	// Multiple decimal points — second dot treated as separator
	test("12.3.4", () => expect(cleanNumericInput("12.3.4")).toBe("12.34"));
	test("1.2.3.4", () => expect(cleanNumericInput("1.2.3.4")).toBe("1.234"));

	// Currency symbols and special chars
	test("$100", () => expect(cleanNumericInput("$100")).toBe("100"));
	test("€1,000", () => expect(cleanNumericInput("€1,000")).toBe("1000"));
	test("100%", () => expect(cleanNumericInput("100%")).toBe("100"));

	// Trailing separator with no digit after
	test("100,", () => expect(cleanNumericInput("100,")).toBe("100"));
	test("100.", () => expect(cleanNumericInput("100.")).toBe("100."));

	// Just a dot
	test(".", () => expect(cleanNumericInput(".")).toBe("."));

	// Negative sign (non-alpha, stripped as separator if digit follows)
	test("-5", () => expect(cleanNumericInput("-5")).toBe("5"));
	test("-1,000", () => expect(cleanNumericInput("-1,000")).toBe("1000"));

	// Whitespace
	test("  123  ", () => expect(cleanNumericInput("  123  ")).toBe("123"));
	test("1 0 0", () => expect(cleanNumericInput("1 0 0")).toBe("100"));
	test("  1,000  ", () => expect(cleanNumericInput("  1,000  ")).toBe("1000"));
	test("  .5  ", () => expect(cleanNumericInput("  .5  ")).toBe(".5"));

	// Tab and mixed whitespace
	test("tab 123", () => expect(cleanNumericInput("\t123\t")).toBe("123"));
	test("newline 123", () => expect(cleanNumericInput("\n123\n")).toBe("123"));

	// Plus sign
	test("+5", () => expect(cleanNumericInput("+5")).toBe("5"));
	test("+1,000", () => expect(cleanNumericInput("+1,000")).toBe("1000"));

	// Mixed separators
	test("1_000,000.50", () => expect(cleanNumericInput("1_000,000.50")).toBe("1000000.50"));

	// Parentheses (accounting negative)
	test("(100)", () => expect(cleanNumericInput("(100)")).toBe("100"));

	// Only separators
	test(",", () => expect(cleanNumericInput(",")).toBe(""));
	test(",,", () => expect(cleanNumericInput(",,")).toBe(""));
	test("_", () => expect(cleanNumericInput("_")).toBe(""));

	// Only alpha
	test("abc", () => expect(cleanNumericInput("abc")).toBe(""));
	test("NaN", () => expect(cleanNumericInput("NaN")).toBe(""));
	test("Infinity", () => expect(cleanNumericInput("Infinity")).toBe(""));

	// Very large numbers
	test("999,999,999.99", () => expect(cleanNumericInput("999,999,999.99")).toBe("999999999.99"));
	test("1_000_000_000", () => expect(cleanNumericInput("1_000_000_000")).toBe("1000000000"));

	// Pasted from spreadsheet with trailing whitespace/newline
	test("1234\\n", () => expect(cleanNumericInput("1234\n")).toBe("1234"));
	test("\\t5000\\t", () => expect(cleanNumericInput("\t5000\t")).toBe("5000"));

	// Unicode non-breaking space
	test("1\u00A0000", () => expect(cleanNumericInput("1\u00A0000")).toBe("1000"));

	// Hash, slash, other symbols before digits
	test("#100", () => expect(cleanNumericInput("#100")).toBe("100"));
	test("/100", () => expect(cleanNumericInput("/100")).toBe("100"));

	// Zero edge cases
	test("0.00", () => expect(cleanNumericInput("0.00")).toBe("0.00"));
	test("00.5", () => expect(cleanNumericInput("00.5")).toBe("00.5"));
	test("000", () => expect(cleanNumericInput("000")).toBe("000"));
	test("0,000", () => expect(cleanNumericInput("0,000")).toBe("0000"));
});

describe("simulateOnChange (clean + Number)", () => {
	test("empty → undefined", () => {
		expect(simulateOnChange("")).toEqual({ display: "", value: undefined });
	});
	test("just dot → undefined", () => {
		expect(simulateOnChange(".")).toEqual({ display: ".", value: undefined });
	});
	test("123 → 123", () => {
		expect(simulateOnChange("123")).toEqual({ display: "123", value: 123 });
	});
	test("1.5 → 1.5", () => {
		expect(simulateOnChange("1.5")).toEqual({ display: "1.5", value: 1.5 });
	});
	test("0.0 → display 0.0, value 0", () => {
		expect(simulateOnChange("0.0")).toEqual({ display: "0.0", value: 0 });
	});
	test("1,000 → 1000", () => {
		expect(simulateOnChange("1,000")).toEqual({ display: "1000", value: 1000 });
	});
	test("1_000 → 1000", () => {
		expect(simulateOnChange("1_000")).toEqual({ display: "1000", value: 1000 });
	});
	test("1abc → 1", () => {
		expect(simulateOnChange("1abc")).toEqual({ display: "1", value: 1 });
	});
	test("$100 → 100", () => {
		expect(simulateOnChange("$100")).toEqual({ display: "100", value: 100 });
	});
	test("1,234.56 → 1234.56", () => {
		expect(simulateOnChange("1,234.56")).toEqual({ display: "1234.56", value: 1234.56 });
	});
	test("  500  → 500 (trimmed)", () => {
		expect(simulateOnChange("  500  ")).toEqual({ display: "500", value: 500 });
	});
	test("abc → empty, undefined", () => {
		expect(simulateOnChange("abc")).toEqual({ display: "", value: undefined });
	});
	test("999,999,999.99 → 999999999.99", () => {
		expect(simulateOnChange("999,999,999.99")).toEqual({ display: "999999999.99", value: 999999999.99 });
	});
	test("0.01 → 0.01", () => {
		expect(simulateOnChange("0.01")).toEqual({ display: "0.01", value: 0.01 });
	});
	test("-5 → 5 (negative sign stripped)", () => {
		expect(simulateOnChange("-5")).toEqual({ display: "5", value: 5 });
	});
});

describe("simulateOnBlur (normalize display)", () => {
	test("empty → empty, undefined", () => {
		expect(simulateOnBlur("")).toEqual({ display: "", value: undefined });
	});
	test("dot → empty, undefined", () => {
		expect(simulateOnBlur(".")).toEqual({ display: "", value: undefined });
	});
	test("0. → 0", () => {
		expect(simulateOnBlur("0.")).toEqual({ display: "0", value: 0 });
	});
	test("1.0 → 1 (Number normalizes trailing zero)", () => {
		expect(simulateOnBlur("1.0")).toEqual({ display: "1", value: 1 });
	});
	test("1.50 → 1.5", () => {
		expect(simulateOnBlur("1.50")).toEqual({ display: "1.5", value: 1.5 });
	});
	test("0100 → 100 (leading zero stripped)", () => {
		expect(simulateOnBlur("0100")).toEqual({ display: "100", value: 100 });
	});
	test("1000 → 1000", () => {
		expect(simulateOnBlur("1000")).toEqual({ display: "1000", value: 1000 });
	});
});