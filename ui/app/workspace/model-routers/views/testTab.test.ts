import { describe, expect, test } from "vitest";
import { decidedBy, parseTestStatements } from "./testTab";

describe("test statements", () => {
	test("reads one statement per line, skipping blank lines", () => {
		expect(parseTestStatements("hello\n\n  summarize this thread  \n")).toEqual([
			{ id: "1", text: "hello" },
			{ id: "2", text: "summarize this thread" },
		]);
	});

	test("takes a leading tier, in any case, as the tier the statement should get", () => {
		expect(parseTestStatements("simple: hi\nCOMPLEX:design a cache\nMedium : fix this function")).toEqual([
			{ id: "1", text: "hi", expected_tier: "SIMPLE" },
			{ id: "2", text: "design a cache", expected_tier: "COMPLEX" },
			{ id: "3", text: "fix this function", expected_tier: "MEDIUM" },
		]);
	});

	test("drops a line that is only a label and keeps a colon that is not one", () => {
		expect(parseTestStatements("SIMPLE:\nnote: keep this")).toEqual([{ id: "2", text: "note: keep this" }]);
	});
});

describe("how a statement got its tier", () => {
	test("names the mechanism in the UI language, not its wire value", () => {
		expect(decidedBy({ mechanism: "semantic", score: 0.912 })).toBe("Semantic · 0.91");
		expect(decidedBy({ mechanism: "skipped" })).toBe("Skipped");
	});

	test("names the model that answered a decision or llm classification", () => {
		expect(decidedBy({ mechanism: "decision", model: "typesafe/jev-latest", confidence: 0.8 })).toBe(
			"Decision model · typesafe/jev-latest · 0.80",
		);
		expect(decidedBy({ mechanism: "llm", model: "openai/gpt-4o-mini" })).toBe("LLM · openai/gpt-4o-mini");
	});

	test("shows an unknown mechanism as the gateway sent it", () => {
		expect(decidedBy({ mechanism: "future-mechanism" })).toBe("future-mechanism");
	});
});