import { describe, expect, it } from "vitest";
import { addPattern, modelAccessPlaceholder, resolveWildcardSelection, summarizeModelAccess, validateModelRegex } from "./utils";

describe("validateModelRegex", () => {
	it("validates patterns the way the backend will", () => {
		expect(validateModelRegex("^gpt-4.*")).toBeNull();
		expect(validateModelRegex("  ^gpt-4.*  ")).toBeNull();
		expect(validateModelRegex("")).not.toBeNull();
		expect(validateModelRegex("   ")).not.toBeNull();
		expect(validateModelRegex("(")).not.toBeNull();
		expect(validateModelRegex("(?<=x)y")).not.toBeNull();
	});
	it("refuses the wildcard as a pattern", () => {
		expect(validateModelRegex("*")).not.toBeNull();
	});
});

describe("resolveWildcardSelection", () => {
	it("collapses to the wildcard when it is newly selected", () => {
		expect(resolveWildcardSelection(["gpt-4o"], ["gpt-4o", "*"])).toEqual(["*"]);
	});
	it("drops the wildcard when something else is added next to it", () => {
		expect(resolveWildcardSelection(["*"], ["*", "gpt-4o"])).toEqual(["gpt-4o"]);
	});
	it("passes other selections through", () => {
		expect(resolveWildcardSelection(["a"], ["a", "b"])).toEqual(["a", "b"]);
	});
});

describe("addPattern", () => {
	it("appends a trimmed pattern", () => {
		expect(addPattern([], "^gpt-4.*")).toEqual(["^gpt-4.*"]);
		expect(addPattern(["^gpt-4.*"], " ^claude.* ")).toEqual(["^gpt-4.*", "^claude.*"]);
	});
	it("does not duplicate an existing pattern", () => {
		expect(addPattern(["^gpt-4.*"], "^gpt-4.*")).toEqual(["^gpt-4.*"]);
	});
	it("leaves the exact list alone", () => {
		const models = ["*"];
		expect(addPattern([], "^gpt-4.*")).toEqual(["^gpt-4.*"]);
		expect(models).toEqual(["*"]);
	});
});

describe("summaries and placeholders", () => {
	it("summarises for the collapsed header", () => {
		expect(summarizeModelAccess(["*"], [], "allow")).toBe("All models");
		expect(summarizeModelAccess(["*"], ["^x"], "allow")).toBe("All models");
		expect(summarizeModelAccess([], [], "allow")).toBe("Deny all");
		expect(summarizeModelAccess([], [], "block")).toBe("No blocked models");
		expect(summarizeModelAccess(["a", "b"], ["x"], "allow")).toBe("2 models, 1 pattern");
		expect(summarizeModelAccess([], ["x", "y"], "block")).toBe("2 patterns");
		expect(summarizeModelAccess(["regex:^gpt.*"], [], "allow")).toBe("1 model");
	});

	it("keeps the placeholder wording per mode", () => {
		expect(modelAccessPlaceholder(["*"], "allow")).toBe("All models allowed");
		expect(modelAccessPlaceholder([], "allow")).toBe("No models (deny all)");
		expect(modelAccessPlaceholder([], "allow", ["^gpt.*"])).toBe("Add model…");
		expect(modelAccessPlaceholder(["*"], "block")).toBe("All models blocked");
		expect(modelAccessPlaceholder([], "block")).toBe("No blocked models");
	});
});
