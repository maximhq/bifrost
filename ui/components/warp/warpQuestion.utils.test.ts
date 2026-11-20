import { describe, expect, it } from "vitest";
import {
	acceptsOptionShortcuts,
	confirmArgumentRows,
	humanizeToolName,
	isInteractiveTarget,
	questionCardKey,
	type KeyEventTarget,
} from "./warpQuestion.utils";

// Plain objects: vitest here has no DOM environment.
function node(tagName: string, attrs: Record<string, string> = {}, parent: KeyEventTarget | null = null): KeyEventTarget {
	return {
		tagName,
		getAttribute: (name: string) => attrs[name] ?? null,
		parentElement: parent,
	};
}

describe("isInteractiveTarget", () => {
	it("treats buttons, links and selects as interactive", () => {
		expect(isInteractiveTarget(node("BUTTON"))).toBe(true);
		expect(isInteractiveTarget(node("A", { href: "/x" }))).toBe(true);
		expect(isInteractiveTarget(node("SELECT"))).toBe(true);
		expect(isInteractiveTarget(node("DIV", { role: "button" }))).toBe(true);
	});

	it("treats a child of a control as interactive", () => {
		expect(isInteractiveTarget(node("SPAN", {}, node("BUTTON")))).toBe(true);
	});

	it("ignores an anchor with no href", () => {
		expect(isInteractiveTarget(node("A"))).toBe(false);
	});

	it("leaves ordinary containers alone, so the card still gets its keys", () => {
		expect(isInteractiveTarget(node("DIV"))).toBe(false);
		expect(isInteractiveTarget(null)).toBe(false);
		expect(isInteractiveTarget(undefined)).toBe(false);
	});
});
describe("acceptsOptionShortcuts", () => {
	it("refuses keyboard picks on a confirmation card, so Enter cannot run a write", () => {
		expect(acceptsOptionShortcuts({ kind: "confirm" })).toBe(false);
	});
	it("keeps them for every other question", () => {
		for (const kind of [undefined, "time_range", "scope", "other"]) {
			expect(acceptsOptionShortcuts({ kind })).toBe(true);
		}
	});
});
describe("humanizeToolName", () => {
	it("splits camel case and keeps acronyms", () => {
		expect(humanizeToolName("createVirtualKey")).toBe("Create virtual key");
		expect(humanizeToolName("createMCPClient")).toBe("Create MCP client");
		expect(humanizeToolName("delete_provider-key")).toBe("Delete provider key");
		expect(humanizeToolName("")).toBe("");
	});
});

describe("confirmArgumentRows", () => {
	it("lists top-level fields, nested values as JSON", () => {
		expect(confirmArgumentRows('{"name":"by warp","budget":{"max_limit":10},"active":true,"note":"","team":null}')).toEqual([
			{ key: "name", value: "by warp", structured: false, empty: false },
			{ key: "budget", value: '{"max_limit":10}', structured: true, empty: false },
			{ key: "active", value: "true", structured: false, empty: false },
			{ key: "note", value: '""', structured: false, empty: true },
			{ key: "team", value: "null", structured: false, empty: true },
		]);
	});

	it("returns null for anything that is not a JSON object", () => {
		expect(confirmArgumentRows("not json")).toBeNull();
		expect(confirmArgumentRows("[1,2]")).toBeNull();
		expect(confirmArgumentRows("3")).toBeNull();
		expect(confirmArgumentRows("{}")).toEqual([]);
	});
});

describe("questionCardKey", () => {
	it("keeps one key for the same question and gives an equal new question its own", () => {
		const card = { question: "Run createVirtualKey?", kind: "confirm", options: [] };
		const again = { ...card };
		expect(questionCardKey(card)).toBe(questionCardKey(card));
		expect(questionCardKey(again)).not.toBe(questionCardKey(card));
	});
});