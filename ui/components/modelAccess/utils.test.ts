import { beforeAll, describe, expect, it } from "vitest";
import i18n from "@/lib/i18n";
import {
	addPattern,
	modelAccessPlaceholder,
	removePattern,
	replaceModels,
	resolveWildcardSelection,
	splitModelAccess,
	summarizeGrantModelAccess,
	summarizeModelAccess,
	templateGroupRefs,
	validateModelRegex,
	validatePatternAlias,
} from "./utils";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

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

	it("reflects blocked models in the combined grant summary (#7634)", () => {
		expect(summarizeGrantModelAccess(["*"], [])).toBe("All models");
		expect(summarizeGrantModelAccess(["*"], ["a", "b"])).toBe("All models · 2 models blocked");
		expect(summarizeGrantModelAccess(["*"], ["a", "regex:^x"])).toBe("All models · 1 model, 1 pattern blocked");
		expect(summarizeGrantModelAccess(["a", "b", "c"], ["a"])).toBe("3 models · 1 model blocked");
		// The block list wins over the allow list, so a blocked "*" denies everything.
		expect(summarizeGrantModelAccess(["*"], ["*"])).toBe("All models blocked");
		expect(summarizeGrantModelAccess(["a"], ["*"])).toBe("All models blocked");
		// An empty allow list already denies everything; the block list adds nothing.
		expect(summarizeGrantModelAccess([], ["a"])).toBe("Deny all");
		expect(summarizeGrantModelAccess(["*"], undefined)).toBe("All models");
	});

	it("keeps the placeholder wording per mode", () => {
		expect(modelAccessPlaceholder(["*"], "allow")).toBe("All models allowed");
		expect(modelAccessPlaceholder([], "allow")).toBe("No models (deny all)");
		expect(modelAccessPlaceholder(["regex:^gpt.*"], "allow")).toBe("Add model…");
		expect(modelAccessPlaceholder(["*"], "block")).toBe("All models blocked");
		expect(modelAccessPlaceholder([], "block")).toBe("No blocked models");
	});
});
describe("validatePatternAlias", () => {
	it("ignores plain deployment names", () => {
		expect(validatePatternAlias("claude-opus-4-8", "global.anthropic.$9")).toBeNull();
	});
	it("accepts templates that reference existing groups", () => {
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.$1")).toBeNull();
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.${1}", "$1")).toBeNull();
		expect(validatePatternAlias("regex:(?P<m>gpt-.*)", "openai.${m}")).toBeNull();
		expect(validatePatternAlias("regex:mistral-.*", "mistral.fixed-$$")).toBeNull();
	});
	it("rejects bad patterns and missing groups", () => {
		expect(validatePatternAlias("regex:", "x")).not.toBeNull();
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.$2")).toContain("$2");
		expect(validatePatternAlias("regex:(claude-.*)", "$1x")).toContain("$1x");
		expect(validatePatternAlias("regex:(claude-.*)", "$1", "${m}")).toContain("$m");
	});
	it("accepts Go inline flags that JavaScript cannot parse", () => {
		expect(validatePatternAlias("regex:(?i)(claude-.*)", "global.anthropic.$1")).toBeNull();
		expect(validatePatternAlias("regex:(?i)(claude-.*)", "global.anthropic.$2")).toContain("$2");
		expect(validatePatternAlias("regex:(?s-i:claude)-(.*)", "x.$1")).toBeNull();
		expect(validatePatternAlias("regex:(?s-i:claude)-(.*)", "x.$2")).toContain("$2");
		expect(validateModelRegex("(?i)claude-.*")).toBeNull();
		expect(validateModelRegex("(?U)claude-.*?")).toBeNull();
	});
	it("accepts Go quoted literals that JavaScript cannot parse", () => {
		expect(validatePatternAlias("regex:\\Q(\\E(claude-.*)", "x.$1")).toBeNull();
		// Quoted text opens no group and names none.
		expect(validatePatternAlias("regex:\\Q(\\E(claude-.*)", "x.$2")).toContain("$2");
		expect(validatePatternAlias("regex:\\Q(?P<m>\\E(claude-.*)", "x.$m")).toContain("$m");
		expect(validateModelRegex("\\Qa.b(\\E-.*")).toBeNull();
		// Without a closing \\E the quote runs to the end of the pattern.
		expect(validateModelRegex("claude-\\Q(")).toBeNull();
		expect(validateModelRegex("\\Q\\E(")).not.toBeNull();
		// An escaped backslash before Q starts no quote.
		expect(validateModelRegex("\\\\Q(")).not.toBeNull();
		// A quoted \\k< is literal text, a bare one is still a named backreference.
		expect(validatePatternAlias("regex:\\Q\\k<\\E(claude-.*)", "x.$1")).toBeNull();
		expect(validateModelRegex("(?P<a>x)\\k<a>")).not.toBeNull();
	});
	it("ignores group syntax written inside a character class", () => {
		expect(validatePatternAlias("regex:[(?P<m>)](claude-.*)", "global.anthropic.$m")).toContain("$m");
		expect(validatePatternAlias("regex:[(?P<m>)](?P<n>claude-.*)", "x.$n")).toBeNull();
		// In Go a leading "]" is literal, so the class runs on to the next one.
		expect(validatePatternAlias("regex:[](?P<m>)](claude-.*)", "x.$m")).toContain("$m");
		expect(validatePatternAlias("regex:[^](?P<m>)](claude-.*)", "x.$m")).toContain("$m");
		// An escaped bracket opens no class, and a POSIX class closes where Go closes it.
		expect(validatePatternAlias("regex:\\[(?P<m>claude-.*)", "x.$m")).toBeNull();
		expect(validatePatternAlias("regex:[[:alpha:]](?P<m>claude-.*)", "x.$m")).toBeNull();
	});
	it("leaves syntax JavaScript cannot judge to the gateway", () => {
		// Go reads \\123 as an octal escape and \\Q\\1\\E as literal text; neither is a backreference.
		expect(validatePatternAlias("regex:\\123(claude-.*)", "x.$1")).toBeNull();
		expect(validatePatternAlias("regex:\\Q\\1\\E(claude-.*)", "x.$1")).toBeNull();
		expect(validatePatternAlias("regex:[(?=](claude-.*)", "x.$1")).toBeNull();
		// A missing group is still flagged whenever the pattern can be read.
		expect(validatePatternAlias("regex:\\123(claude-.*)", "x.$2")).toContain("$2");
		expect(validatePatternAlias("regex:\\Q\\1\\E(claude-.*)", "x.$2")).toContain("$2");
		// A pattern that does not parse here is not judged at all.
		expect(validatePatternAlias("regex:(claude-", "x.$1")).toBeNull();
	});
	it("still rejects what is wrong in any regex dialect", () => {
		expect(validatePatternAlias("regex:", "x")).not.toBeNull();
		expect(validatePatternAlias("regex:   ", "x")).not.toBeNull();
		expect(validatePatternAlias("regex:*", "x")).not.toBeNull();
	});
	it("still rejects flag groups Go does not accept", () => {
		expect(validateModelRegex("(?)claude")).not.toBeNull();
		expect(validateModelRegex("(?x)claude")).not.toBeNull();
		expect(validateModelRegex("(?i")).not.toBeNull();
	});
	it("reads group references the way the gateway does", () => {
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.$1é")).toContain("$1é");
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.$é")).toContain("$é");
		expect(validatePatternAlias("regex:(claude-.*)", "global.anthropic.${1}é")).toBeNull();
		expect(validatePatternAlias("regex:(claude-.*)", "x.$01")).toContain("$01");
		expect(validatePatternAlias("regex:(claude-.*)", "x.${01}")).toContain("$01");
		expect(validatePatternAlias("regex:(?P<01>claude-.*)", "x.$01")).toBeNull();
		expect(validatePatternAlias("regex:(claude-.*)", "x.$0")).toBeNull();
		expect(templateGroupRefs("a.$1é/${m}")).toEqual(["1é", "m"]);
		// Nine digits are a group number to Go, ten are a name.
		expect(validatePatternAlias("regex:(?P<123456789>claude-.*)", "x.$123456789")).toContain("$123456789");
		expect(validatePatternAlias("regex:(?P<1234567890>claude-.*)", "x.$1234567890")).toBeNull();
	});
	it("leaves an escaped parenthesis before a question mark alone", () => {
		expect(validatePatternAlias("regex:\\(?i\\)(claude-.*)", "x.$1")).toBeNull();
		expect(validatePatternAlias("regex:\\(?i\\)(claude-.*)", "x.$2")).toContain("$2");
	});
});

describe("templateGroupRefs", () => {
	it("parses refs like Go regexp.Expand", () => {
		expect(templateGroupRefs("a.$1.${name}.$$.$x_y")).toEqual(["1", "name", "x_y"]);
	});
});