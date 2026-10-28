import { describe, expect, it } from "vitest";
import { renameHeaderKeepingCleared, showsAddRow } from "./headersTable";

describe("renameHeaderKeepingCleared", () => {
	const headers = { "X-Amz-Target": "Textract.Analyze*", "X-Kind": "poll" };

	it("keeps a cleared row in place with its value, so validation sees it instead of the entry vanishing", () => {
		expect(Object.entries(renameHeaderKeepingCleared(headers, 0, "", "Textract.Analyze*")!)).toEqual([
			["", "Textract.Analyze*"],
			["X-Kind", "poll"],
		]);
	});

	it("finishes a rename at the same position", () => {
		const cleared = renameHeaderKeepingCleared(headers, 0, "", "Textract.Analyze*")!;
		expect(Object.entries(renameHeaderKeepingCleared(cleared, 0, "X-Target", "Textract.Analyze*")!)).toEqual([
			["X-Target", "Textract.Analyze*"],
			["X-Kind", "poll"],
		]);
	});

	it("appends a name typed into the trailing empty row, and ignores clearing it", () => {
		expect(Object.entries(renameHeaderKeepingCleared(headers, 2, "X-New", "")!)).toEqual([
			["X-Amz-Target", "Textract.Analyze*"],
			["X-Kind", "poll"],
			["X-New", ""],
		]);
		expect(renameHeaderKeepingCleared(headers, 2, "", "")).toBe(headers);
	});

	it("refuses a second cleared row, which one object cannot hold", () => {
		const cleared = renameHeaderKeepingCleared(headers, 0, "", "Textract.Analyze*")!;
		expect(renameHeaderKeepingCleared(cleared, 1, "", "poll")).toBeUndefined();
	});
});
describe("showsAddRow", () => {
	it("hides the add row while a cleared row holds the empty name, so the two cannot overwrite each other", () => {
		const headers = { "X-Amz-Target": "Textract.Analyze*" };
		expect(showsAddRow(headers, true)).toBe(true);
		const cleared = renameHeaderKeepingCleared(headers, 0, "", "Textract.Analyze*")!;
		expect(showsAddRow(cleared, true)).toBe(false);
		// Callers without keepClearedNames never hold an "" entry for long, and keep their add row.
		expect(showsAddRow(cleared, false)).toBe(true);
	});
});