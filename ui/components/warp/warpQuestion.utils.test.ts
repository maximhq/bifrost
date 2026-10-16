import { describe, expect, it } from "vitest";
import { isInteractiveTarget, type KeyEventTarget } from "./warpQuestion.utils";

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