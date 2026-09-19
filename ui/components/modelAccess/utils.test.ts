import { describe, expect, it } from "vitest";
import {
	addPattern,
	modelAccessPlaceholder,
	removePattern,
	replaceModels,
	resolveWildcardSelection,
	splitModelAccess,
	summarizeModelAccess,
	validateModelRegex,
} from "./utils";

describe("splitModelAccess", () => {
	it("separates names from regex: entries and strips the prefix", () => {
		expect(splitModelAccess(["gpt-4o", "regex:^claude-3-.*", "*"])).toEqual({ models: ["gpt-4o", "*"], patterns: ["^claude-3-.*"] });
		expect(splitModelAccess(undefined)).toEqual({ models: [], patterns: [] });
	});
	it("only treats the lowercase prefix as a pattern", () => {
		expect(splitModelAccess(["REGEX:x"])).toEqual({ models: ["REGEX:x"], patterns: [] });
	});
});

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
	it("follows Go RE2 on named groups and backreferences", () => {
		expect(validateModelRegex("(?P<family>gpt-4).*")).toBeNull();
		expect(validateModelRegex("(?<family>gpt-4).*")).toBeNull();
		expect(validateModelRegex("(?P<1>gpt).*")).toBeNull();
		expect(validateModelRegex("(?<1>gpt).*")).toBeNull();
		expect(validateModelRegex("(?<a>x)\\k<a>")).not.toBeNull();
		expect(validateModelRegex(String.raw`^foo\\k<bar>$`)).toBeNull();
		expect(validateModelRegex(String.raw`^foo\\\k<bar>$`)).not.toBeNull();
		expect(validateModelRegex("(x)\\1")).not.toBeNull();
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

describe("replaceModels", () => {
	it("keeps the patterns while the names change", () => {
		expect(replaceModels(["a", "regex:^x"], ["a", "b"])).toEqual(["a", "b", "regex:^x"]);
		expect(replaceModels(["a", "regex:^x"], [])).toEqual(["regex:^x"]);
	});
	it("clears the patterns when the wildcard is picked, since * must stand alone", () => {
		expect(replaceModels(["a", "regex:^x"], ["a", "*"])).toEqual(["*"]);
	});
});

describe("addPattern and removePattern", () => {
	it("appends a trimmed pattern as a regex: entry", () => {
		expect(addPattern([], "^gpt-4.*")).toEqual(["regex:^gpt-4.*"]);
		expect(addPattern(["gpt-4o"], " ^claude.* ")).toEqual(["gpt-4o", "regex:^claude.*"]);
	});
	it("does not duplicate an existing pattern", () => {
		expect(addPattern(["regex:^gpt-4.*"], "^gpt-4.*")).toEqual(["regex:^gpt-4.*"]);
	});
	it("drops a lone wildcard when a pattern is added", () => {
		expect(addPattern(["*"], "^gpt-4.*")).toEqual(["regex:^gpt-4.*"]);
	});
	it("removes only the matching pattern", () => {
		expect(removePattern(["gpt-4o", "regex:^a", "regex:^b"], "^a")).toEqual(["gpt-4o", "regex:^b"]);
	});
});

describe("summaries and placeholders", () => {
	it("summarises for the collapsed header", () => {
		expect(summarizeModelAccess(["*"], "allow")).toBe("All models");
		expect(summarizeModelAccess([], "allow")).toBe("Deny all");
		expect(summarizeModelAccess([], "block")).toBe("No blocked models");
		expect(summarizeModelAccess(["a", "b", "regex:x"], "allow")).toBe("2 models, 1 pattern");
		expect(summarizeModelAccess(["regex:x", "regex:y"], "block")).toBe("2 patterns");
	});

	it("keeps the placeholder wording per mode", () => {
		expect(modelAccessPlaceholder(["*"], "allow")).toBe("All models allowed");
		expect(modelAccessPlaceholder([], "allow")).toBe("No models (deny all)");
		expect(modelAccessPlaceholder(["regex:^gpt.*"], "allow")).toBe("Add model…");
		expect(modelAccessPlaceholder(["*"], "block")).toBe("All models blocked");
		expect(modelAccessPlaceholder([], "block")).toBe("No blocked models");
	});
});