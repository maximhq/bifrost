import { describe, expect, it } from "vitest";
import { EMPTY_TITLE_ENTRY, claimTitle, releaseTitle } from "./topbarContext.utils";

const pageA = Symbol("page-a");
const pageB = Symbol("page-b");

describe("claimTitle", () => {
	it("records the value and its owner", () => {
		expect(claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs")).toEqual({ value: "Logs", owner: pageA });
	});

	it("lets a later page take over the title", () => {
		const owned = claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs");
		expect(claimTitle(owned, pageB, "Budgets & Limits")).toEqual({ value: "Budgets & Limits", owner: pageB });
	});

	it("returns the same object when nothing changed, so the provider does not re-render", () => {
		const owned = claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs");
		expect(claimTitle(owned, pageA, "Logs")).toBe(owned);
	});
});

describe("releaseTitle", () => {
	it("clears the title when the owner is still the one unmounting", () => {
		const owned = claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs");
		expect(releaseTitle(owned, pageA)).toEqual(EMPTY_TITLE_ENTRY);
	});

	it("leaves a title set by a different page alone", () => {
		// Route change: B mounts and claims before A's cleanup runs.
		const handedOver = claimTitle(claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs"), pageB, "Budgets & Limits");
		expect(releaseTitle(handedOver, pageA)).toEqual({ value: "Budgets & Limits", owner: pageB });
	});

	it("leaves the incoming page's title alone even when both pages use the same text", () => {
		// The reason ownership cannot be inferred from the title string: A and B
		// both call themselves "Logs", so comparing text would make A's cleanup
		// clear the title B just claimed, dropping the topbar to the
		// route-derived fallback.
		const handedOver = claimTitle(claimTitle(EMPTY_TITLE_ENTRY, pageA, "Logs"), pageB, "Logs");
		expect(releaseTitle(handedOver, pageA)).toEqual({ value: "Logs", owner: pageB });
	});
});