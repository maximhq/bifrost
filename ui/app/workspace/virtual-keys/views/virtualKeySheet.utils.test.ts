import { describe, expect, it } from "vitest";
import { diffVmcpAssignments, vmcpAssignmentsDirty } from "./virtualKeySheet.utils";

describe("diffVmcpAssignments", () => {
	it("returns empty diffs when nothing changed (order-insensitive)", () => {
		expect(diffVmcpAssignments([1, 2, 3], [3, 2, 1])).toEqual({ toAttach: [], toDetach: [] });
	});

	it("attaches ids added and detaches ids removed", () => {
		expect(diffVmcpAssignments([1, 2], [2, 3])).toEqual({ toAttach: [3], toDetach: [1] });
	});

	it("attaches all when starting from empty", () => {
		expect(diffVmcpAssignments([], [5, 6])).toEqual({ toAttach: [5, 6], toDetach: [] });
	});

	it("detaches all when clearing", () => {
		expect(diffVmcpAssignments([5, 6], [])).toEqual({ toAttach: [], toDetach: [5, 6] });
	});

	it("collapses duplicates within an input", () => {
		expect(diffVmcpAssignments([1, 1], [1, 2, 2])).toEqual({ toAttach: [2], toDetach: [] });
	});
});

describe("vmcpAssignmentsDirty", () => {
	it("is false for equal sets regardless of order", () => {
		expect(vmcpAssignmentsDirty([1, 2, 3], [3, 1, 2])).toBe(false);
	});

	it("is true when an id is added or removed", () => {
		expect(vmcpAssignmentsDirty([1, 2], [1, 2, 3])).toBe(true);
		expect(vmcpAssignmentsDirty([1, 2], [1])).toBe(true);
	});
});
