import { describe, expect, it } from "vitest";
import { exposedByRule, isExposed, nextExposeList } from "./mcpExposeList.utils";

describe("isExposed", () => {
	it("serves everything when unset or ['*']", () => {
		expect(isExposed(undefined, "review")).toBe(true);
		expect(isExposed(null, "review")).toBe(true);
		expect(isExposed(["*"], "review")).toBe(true);
	});
	it("serves only listed keys otherwise", () => {
		expect(isExposed([], "review")).toBe(false);
		expect(isExposed(["review"], "review")).toBe(true);
		expect(isExposed(["summarize"], "review")).toBe(false);
	});
});

describe("nextExposeList", () => {
	const all = ["review", "summarize"];
	it("turning one off from all lists the rest", () => {
		expect(nextExposeList(undefined, all, "review", false)).toEqual(["summarize"]);
	});
	it("turning the last one back on collapses to ['*']", () => {
		expect(nextExposeList(["summarize"], all, "review", true)).toEqual(["*"]);
	});
	it("turning every one off leaves an empty list", () => {
		expect(nextExposeList(["review"], all, "review", false)).toEqual([]);
	});
});
describe("matching the gateway", () => {
	it("matches plain entries case-insensitively and regex: entries in full", () => {
		expect(isExposed(["Review"], "review")).toBe(true);
		expect(isExposed(["regex:file:///docs/.*"], "file:///docs/a.md")).toBe(true);
		expect(isExposed(["regex:file:///docs/.*"], "file:///other/a.md")).toBe(false);
		expect(isExposed(["regex:("], "review")).toBe(false);
	});
	it("reports items served only by a rule", () => {
		expect(exposedByRule(["regex:rev.*"], "review")).toBe(true);
		expect(exposedByRule(["review", "regex:rev.*"], "summarize")).toBe(false);
		expect(exposedByRule(["*"], "review")).toBe(false);
	});
});

describe("nextExposeList keeps what the switch does not own", () => {
	const all = ["review", "summarize"];
	it("keeps a regex rule when another item is toggled", () => {
		expect(nextExposeList(["regex:sum.*"], all, "review", true)).toEqual(["regex:sum.*", "review"]);
		expect(nextExposeList(["regex:sum.*", "review"], all, "review", false)).toEqual(["regex:sum.*"]);
	});
	it("keeps saved keys missing from discovery and does not collapse to ['*']", () => {
		expect(nextExposeList(["review", "hidden"], all, "summarize", true)).toEqual(["review", "hidden", "summarize"]);
	});
	it("replaces a differently cased entry instead of duplicating it", () => {
		expect(nextExposeList(["Review"], all, "review", false)).toEqual([]);
	});
	it("keeps one entry for keys that differ only by case, as the gateway matches them", () => {
		const cased = ["file:///A", "file:///a", "file:///B"];
		expect(nextExposeList(undefined, cased, "file:///B", false)).toEqual(["file:///A"]);
		expect(nextExposeList(["file:///A"], cased, "file:///B", true)).toEqual(["*"]);
	});
});