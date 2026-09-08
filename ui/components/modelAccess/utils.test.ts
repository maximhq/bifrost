import { describe, expect, it } from "vitest";
import {
	addRegexEntry,
	isRegexEntry,
	modelAccessPlaceholder,
	resolveWildcardSelection,
	splitModelEntries,
	stripRegexPrefix,
	summarizeModelList,
	toRegexEntry,
	validateModelEntry,
	validateModelRegex,
} from "./utils";

describe("regex entry helpers", () => {
	it("detects the regex: prefix literally", () => {
		expect(isRegexEntry("regex:^gpt-4.*")).toBe(true);
		expect(isRegexEntry("REGEX:^gpt-4.*")).toBe(false);
		expect(isRegexEntry("gpt-4o")).toBe(false);
		expect(isRegexEntry("*")).toBe(false);
	});

	it("round-trips a pattern through the prefix", () => {
		expect(toRegexEntry("^gpt-4.*")).toBe("regex:^gpt-4.*");
		expect(stripRegexPrefix("regex:^gpt-4.*")).toBe("^gpt-4.*");
		expect(stripRegexPrefix("gpt-4o")).toBe("gpt-4o");
	});

	it("validates patterns the way the backend will", () => {
		expect(validateModelRegex("^gpt-4.*")).toBeNull();
		expect(validateModelRegex("")).not.toBeNull();
		expect(validateModelRegex("   ")).not.toBeNull();
		expect(validateModelRegex("(")).not.toBeNull();
		expect(validateModelRegex("(?<=x)y")).not.toBeNull();
		expect(validateModelEntry("gpt-4o")).toBeNull();
		expect(validateModelEntry("regex:(")).not.toBeNull();
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

describe("addRegexEntry", () => {
	it("appends a prefixed entry and replaces the wildcard", () => {
		expect(addRegexEntry(["*"], "^gpt-4.*")).toEqual(["regex:^gpt-4.*"]);
		expect(addRegexEntry(["gpt-4o"], " ^claude.* ")).toEqual(["gpt-4o", "regex:^claude.*"]);
	});
	it("does not duplicate an existing pattern", () => {
		expect(addRegexEntry(["regex:^gpt-4.*"], "^gpt-4.*")).toEqual(["regex:^gpt-4.*"]);
	});
});

describe("splitModelEntries / summaries", () => {
	it("separates wildcard, names and patterns", () => {
		expect(splitModelEntries(["*"])).toEqual({ wildcard: true, models: [], regexes: [] });
		expect(splitModelEntries(["gpt-4o", "regex:^claude.*"])).toEqual({
			wildcard: false,
			models: ["gpt-4o"],
			regexes: ["regex:^claude.*"],
		});
	});

	it("summarises for the collapsed header", () => {
		expect(summarizeModelList(["*"], "allow")).toBe("All models");
		expect(summarizeModelList([], "allow")).toBe("Deny all");
		expect(summarizeModelList([], "block")).toBe("No blocked models");
		expect(summarizeModelList(["a", "b", "regex:x"], "allow")).toBe("2 models, 1 pattern");
		expect(summarizeModelList(["regex:x", "regex:y"], "block")).toBe("2 patterns");
	});

	it("keeps the placeholder wording per mode", () => {
		expect(modelAccessPlaceholder(["*"], "allow")).toBe("All models allowed");
		expect(modelAccessPlaceholder([], "allow")).toBe("No models (deny all)");
		expect(modelAccessPlaceholder(["*"], "block")).toBe("All models blocked");
		expect(modelAccessPlaceholder([], "block")).toBe("No blocked models");
	});
});