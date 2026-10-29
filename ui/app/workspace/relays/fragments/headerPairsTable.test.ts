import { relayRouteFormSchema } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";
import { deleteHeaderRow } from "@/components/ui/headersTable";
import { heldConflicts, pairsFromRecord, recordFromPairs } from "./headerPairsTable";

describe("header pairs with a held conflict", () => {
	const record = { "X-A": "a*", "X-B": "b" };

	it("hands the form the conflicting name, so its duplicate check blocks the save", () => {
		// Row 0 was renamed to X-B, which the table holds as a conflict while keeping X-A.
		const pairs = pairsFromRecord(record, new Map([[0, "X-B"]]));
		expect(pairs).toEqual([
			{ name: "X-B", value: "a*" },
			{ name: "X-B", value: "b" },
		]);
		const parsed = relayRouteFormSchema.safeParse({ method: "POST", path: "/", headers: pairs });
		expect(parsed.error?.issues.map((i) => i.message)).toContain("This header is already matched on this route");
	});

	it("gives the table back its last valid names, so its rows stay distinct", () => {
		const pairs = pairsFromRecord(record, new Map([[0, "X-B"]]));
		expect(recordFromPairs(pairs, new Map([[0, "X-A"]]))).toEqual(record);
		expect(Object.keys(recordFromPairs(pairs, new Map([[0, "X-A"]])))).toEqual(["X-A", "X-B"]);
	});

	it("passes pairs straight through when nothing is held", () => {
		expect(pairsFromRecord(record, new Map())).toEqual([
			{ name: "X-A", value: "a*" },
			{ name: "X-B", value: "b" },
		]);
		expect(recordFromPairs(pairsFromRecord(record, new Map()), new Map())).toEqual(record);
	});
});
describe("deleting rows around a held conflict", () => {
	it("keeps each row's name and value when a row above a held conflict is deleted (the reported sequence)", () => {
		let record: Record<string, string> = { "X-A": "a", "X-B": "b", "X-C": "c" };
		// Rename X-C to X-A: the table holds row 2 as a duplicate and keeps X-C in its value.
		let held = heldConflicts(new Map([[2, "X-A"]]), record);
		expect(pairsFromRecord(record, held.conflicts)).toEqual([
			{ name: "X-A", value: "a" },
			{ name: "X-B", value: "b" },
			{ name: "X-A", value: "c" },
		]);
		// Delete X-B: the conflict moves to row 1 together with the post-delete rows.
		const afterDelete = deleteHeaderRow(record, held.conflicts, 1);
		record = afterDelete.value;
		held = heldConflicts(afterDelete.conflicts, record);
		expect(held.originals).toEqual(new Map([[1, "X-C"]]));
		const pairs = pairsFromRecord(record, held.conflicts);
		expect(pairs).toEqual([
			{ name: "X-A", value: "a" },
			{ name: "X-A", value: "c" },
		]);
		expect(recordFromPairs(pairs, held.originals)).toEqual({ "X-A": "a", "X-C": "c" });
		// The conflicted row's trash removes that row, as its message says, rather than restoring X-C.
		const afterRemove = deleteHeaderRow(record, held.conflicts, 1);
		held = heldConflicts(afterRemove.conflicts, afterRemove.value);
		expect(pairsFromRecord(afterRemove.value, held.conflicts)).toEqual([{ name: "X-A", value: "a" }]);
	});

	it("ignores a conflict typed into the empty add row, which has nothing to save yet", () => {
		const held = heldConflicts(new Map([[1, "X-A"]]), { "X-A": "a" });
		expect(held.conflicts.size).toBe(0);
		expect(deleteHeaderRow({ "X-A": "a" }, new Map([[1, "X-A"]]), 1)).toEqual({ value: { "X-A": "a" }, conflicts: new Map() });
	});
});