import { describe, expect, it } from "vitest";
import { isInteractiveTarget, type KeyEventTarget } from "./warpQuestion.utils";

// Plain objects, not DOM nodes: this repo has no DOM environment for vitest,
// and the rule is about shape rather than about rendering.
function node(tagName: string, attrs: Record<string, string> = {}, parent: KeyEventTarget | null = null): KeyEventTarget {
	return {
		tagName,
		getAttribute: (name: string) => attrs[name] ?? null,
		parentElement: parent,
	};
}

describe("isInteractiveTarget", () => {
	// The case that misfired: Skip is a button, and Enter on it picked the
	// highlighted option instead of pressing it.
	it("treats buttons, links and selects as interactive", () => {
		expect(isInteractiveTarget(node("BUTTON"))).toBe(true);
		expect(isInteractiveTarget(node("A", { href: "/x" }))).toBe(true);
		expect(isInteractiveTarget(node("SELECT"))).toBe(true);
		expect(isInteractiveTarget(node("DIV", { role: "button" }))).toBe(true);
	});

	// Focus usually sits on a child of the control, so the check has to walk up.
	it("treats a child of a control as interactive", () => {
		expect(isInteractiveTarget(node("SPAN", {}, node("BUTTON")))).toBe(true);
	});

	// An anchor with no href is not focusable and takes no keys, so it must not
	// swallow the card's shortcuts.
	it("ignores an anchor with no href", () => {
		expect(isInteractiveTarget(node("A"))).toBe(false);
	});

	it("leaves ordinary containers alone, so the card still gets its keys", () => {
		expect(isInteractiveTarget(node("DIV"))).toBe(false);
		expect(isInteractiveTarget(null)).toBe(false);
		expect(isInteractiveTarget(undefined)).toBe(false);
	});
});