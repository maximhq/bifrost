import { describe, expect, it } from "vitest";
import { renameHeaderKeepingCleared, rowOrder, showsAddRow, valueInputInTable } from "./headersTable";

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
describe("valueInputInTable", () => {
	it("finds the row's value input in the table that holds the key input", () => {
		const own = { name: "own" } as unknown as HTMLInputElement;
		const selectors: string[] = [];
		const table = {
			querySelector: (selector: string) => {
				selectors.push(selector);
				return own;
			},
		};
		const from = { closest: (selector: string) => (selector === "table" ? table : null) } as unknown as HTMLElement;
		expect(valueInputInTable(from, 2)).toBe(own);
		expect(selectors).toEqual(['input[data-row="2"][data-column="value"]']);
	});

	it("finds nothing outside a table", () => {
		expect(valueInputInTable({ closest: () => null } as unknown as HTMLElement, 0)).toBeNull();
	});
});

describe("rowOrder", () => {
	it("keeps a row typed as an integer-like name in place, though a plain object lists it first", () => {
		const value = { include_usage: "true", "3": "x" };
		expect(Object.keys(value)).toEqual(["3", "include_usage"]);
		expect(rowOrder(value, ["include_usage", "3"])).toEqual(["include_usage", "3"]);
		// Renaming by display position renames the row the user is typing in, not the one the object lists there.
		expect(renameHeaderKeepingCleared(value, 1, "3d", "x", ["include_usage", "3"])).toEqual({ include_usage: "true", "3d": "x" });
	});

	it("drops removed names and appends new ones", () => {
		expect(rowOrder({ b: 1, c: 2 }, ["a", "b"])).toEqual(["b", "c"]);
	});
});