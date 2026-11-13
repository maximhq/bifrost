import { describe, expect, it } from "vitest";
import { DATASET_COLUMN_TYPES } from "@/lib/types/evalDatasets";
import {
	columnTypesLeft,
	datasetExportUrl,
	defaultImportTargets,
	importMapping,
	isReservedVariableName,
	isStructuredColumn,
	newColumnType,
	parseTags,
	planCellEdits,
	planPaste,
	versionLabel,
	previousCells,
	UndoHistory,
	removeAt,
	pagesCovering,
	buildDatasetTree,
	folderWithDescendants,
	parseFileCell,
	fileCellText,
	isImageFile,
} from "./datasetUtils";

describe("datasetUtils", () => {
	it("knows which columns hold JSON", () => {
		expect(isStructuredColumn("expected_tool_calls")).toBe(true);
		expect(isStructuredColumn("conversation_history")).toBe(true);
		expect(isStructuredColumn("tags")).toBe(true);
		expect(isStructuredColumn("input")).toBe(false);
		expect(isStructuredColumn("variable")).toBe(false);
	});

	it("reserves the variable names typed columns fill", () => {
		expect(isReservedVariableName("input")).toBe(true);
		expect(isReservedVariableName(" history ")).toBe(true);
		expect(isReservedVariableName("conversation_history")).toBe(false);
		expect(isReservedVariableName("context")).toBe(false);
	});

	it("offers each typed column once and variable always", () => {
		const left = columnTypesLeft(DATASET_COLUMN_TYPES, ["input", "variable", "tags"]);
		expect(left).not.toContain("input");
		expect(left).not.toContain("tags");
		expect(left).toContain("variable");
		expect(left).toContain("output");
		// A column being edited keeps its own type on offer.
		expect(columnTypesLeft(DATASET_COLUMN_TYPES, ["input"], "input")).toContain("input");
	});

	it("parses tags without blanks or repeats", () => {
		expect(parseTags(" golden, support ,,golden")).toEqual(["golden", "support"]);
		expect(parseTags("")).toEqual([]);
	});

	it("builds export links", () => {
		expect(datasetExportUrl("d1", "csv")).toMatch(/\/evals\/datasets\/d1\/export\?format=csv$/);
		expect(datasetExportUrl("d1", "jsonl", 3, "s1")).toMatch(/format=jsonl&version=3&split=s1$/);
	});

	it("labels versions", () => {
		expect(versionLabel({ number: 4 })).toBe("v4");
		expect(versionLabel({ number: 5, name: "Baseline" })).toBe("v5 · Baseline");
	});

	it("starts an import from the suggested mapping", () => {
		const targets = defaultImportTargets(["Question", "persona", "output", "", "persona"], ["c1", "", "", "", ""], ["input"]);
		expect(targets).toEqual([
			{ kind: "column", columnId: "c1" },
			{ kind: "new", name: "persona", type: "variable" },
			// A field named like a column type becomes that typed column.
			{ kind: "new", name: "output", type: "output" },
			{ kind: "skip" },
			// The same new name is used once.
			{ kind: "skip" },
		]);
		expect(importMapping(targets)).toEqual([
			{ index: 0, column_id: "c1" },
			{ index: 1, name: "persona", type: "variable" },
			{ index: 2, name: "output", type: "output" },
		]);
		expect(importMapping([{ kind: "skip" }])).toBeNull();
	});

	it("types the columns of an empty dataset from the CSV header", () => {
		const targets = defaultImportTargets(["input", "Expected Output", "history", "input", "notes"], ["", "", "", "", ""]);
		expect(targets).toEqual([
			{ kind: "new", name: "input", type: "input" },
			{ kind: "new", name: "Expected Output", type: "expected_output" },
			{ kind: "new", name: "history", type: "conversation_history" },
			// A dataset has one column of each type, so a second input is left out.
			{ kind: "skip" },
			{ kind: "new", name: "notes", type: "variable" },
		]);
		expect(newColumnType("input", ["input"])).toBeNull();
		expect(newColumnType("  ", [])).toBeNull();
		expect(newColumnType("tags", [])).toBe("tags");
	});
});
describe("planCellEdits", () => {
	const columnIds = ["c-in", "c-out"];
	const rowIds = ["r0", "r1", undefined];

	it("groups edits of the same row into one patch and keeps row order", () => {
		const plan = planCellEdits(
			[
				{ col: 1, row: 1, value: "b" },
				{ col: 0, row: 0, value: "a" },
				{ col: 0, row: 1, value: "" },
			],
			columnIds,
			rowIds,
		);
		expect(plan).toEqual([
			{ index: 0, id: "r0", cells: { "c-in": "a" } },
			{ index: 1, id: "r1", cells: { "c-out": "b", "c-in": "" } },
		]);
	});

	it("drops edits that point at a row or a column that is not there", () => {
		expect(
			planCellEdits(
				[
					{ col: 5, row: 0, value: "x" },
					{ col: 0, row: 2, value: "x" },
					{ col: 0, row: 9, value: "x" },
				],
				columnIds,
				rowIds,
			),
		).toEqual([]);
	});
});

describe("planPaste", () => {
	const columnIds = ["c-in", "c-out"];

	it("writes over existing rows and appends what runs past the last row", () => {
		const plan = planPaste(
			[1, 1],
			[
				["x", "ignored: no third column"],
				["y", ""],
				["z", ""],
			],
			columnIds,
			["r0", "r1"],
			2,
		);
		expect(plan.updates).toEqual([{ index: 1, id: "r1", cells: { "c-out": "x" } }]);
		expect(plan.rows).toEqual([{ "c-out": "y" }, { "c-out": "z" }]);
	});

	it("skips a row inside the dataset that is not loaded, and never appends in its place", () => {
		const plan = planPaste([0, 0], [["a"], ["b"]], columnIds, ["r0", undefined], 2);
		expect(plan.updates).toEqual([{ index: 0, id: "r0", cells: { "c-in": "a" } }]);
		expect(plan.rows).toEqual([]);
	});

	it("turns a paste to the right of the last column into nothing", () => {
		expect(planPaste([2, 0], [["a"]], columnIds, ["r0"], 1)).toEqual({ updates: [], rows: [] });
	});
});
describe("previousCells", () => {
	it("captures what each changed cell held, with an empty string for a cell that was empty", () => {
		const rows: Record<string, Record<string, string>> = { r0: { a: "old", b: "keep" } };
		expect(
			previousCells(
				[
					{ id: "r0", cells: { a: "new", c: "added" } },
					{ id: "gone", cells: { a: "x" } },
				],
				(id) => rows[id],
			),
		).toEqual([{ id: "r0", cells: { a: "old", c: "" } }]);
	});
});

describe("UndoHistory", () => {
	it("moves steps between undo and redo, and a new step clears redo", () => {
		const history = new UndoHistory<string>(2);
		history.push("a");
		history.push("b");
		history.push("c");
		expect(history.takeUndo()).toBe("c");
		history.pushRedo("c");
		expect(history.takeRedo()).toBe("c");
		history.pushUndo("c");
		expect(history.takeUndo()).toBe("c");
		history.pushRedo("c");
		history.push("d");
		expect(history.takeRedo()).toBeUndefined();
		expect(history.takeUndo()).toBe("d");
		expect(history.takeUndo()).toBe("b");
		// The limit of two dropped the oldest step.
		expect(history.takeUndo()).toBeUndefined();
	});
});
describe("removeAt", () => {
	it("moves later rows up and keeps the gaps of rows that are not loaded", () => {
		const rows: (string | undefined)[] = ["a", "b"];
		rows[3] = "d";
		rows[5] = "f";
		const next = removeAt(rows, [1, 3]);
		expect(next[0]).toBe("a");
		expect(1 in next).toBe(false);
		expect(2 in next).toBe(false);
		expect(next[3]).toBe("f");
		expect(next.length).toBe(4);
	});
});

describe("pagesCovering", () => {
	it("lists the pages a row range touches, and nothing for an empty range", () => {
		expect(pagesCovering(0, 99, 100)).toEqual([0]);
		expect(pagesCovering(-20, 250, 100)).toEqual([0, 1, 2]);
		expect(pagesCovering(100, 100, 100)).toEqual([1]);
		expect(pagesCovering(5, 4, 100)).toEqual([]);
	});
});
describe("buildDatasetTree", () => {
	const folders = [
		{ id: "f1", name: "Support" },
		{ id: "f2", name: "Agents" },
		{ id: "f3", name: "Refunds", parent_id: "f1" },
		{ id: "f4", name: "Lost", parent_id: "gone" },
	];
	const datasets = [
		{ id: "d1", name: "Tickets", folder_id: "f1" },
		{ id: "d2", name: "Smoke" },
		{ id: "d3", name: "Orphan", folder_id: "gone" },
		{ id: "d4", name: "Chargebacks", folder_id: "f3" },
	];
	const shape = (nodes: ReturnType<typeof buildDatasetTree<(typeof folders)[number], (typeof datasets)[number]>>["folders"]): unknown =>
		nodes.map((node) => [node.folder.name, node.datasets.map((dataset) => dataset.id), shape(node.folders)]);

	it("nests folders, and shows anything whose parent is gone at the top level", () => {
		const tree = buildDatasetTree(folders, datasets, "");
		expect(shape(tree.folders)).toEqual([
			["Support", ["d1"], [["Refunds", ["d4"], []]]],
			["Agents", [], []],
			["Lost", [], []],
		]);
		expect(tree.root.map((dataset) => dataset.id)).toEqual(["d2", "d3"]);
	});

	it("keeps the path to a match, and everything under a folder whose name matches", () => {
		expect(shape(buildDatasetTree(folders, datasets, "chargeb").folders)).toEqual([["Support", [], [["Refunds", ["d4"], []]]]]);
		expect(shape(buildDatasetTree(folders, datasets, " SUPP ").folders)).toEqual([["Support", ["d1"], [["Refunds", ["d4"], []]]]]);
		const smoke = buildDatasetTree(folders, datasets, "smoke");
		expect(smoke.folders).toEqual([]);
		expect(smoke.root.map((dataset) => dataset.id)).toEqual(["d2"]);
	});

	it("lists a folder with every folder below it", () => {
		expect([...folderWithDescendants(folders, "f1")].sort()).toEqual(["f1", "f3"]);
		expect([...folderWithDescendants(folders, "f2")]).toEqual(["f2"]);
	});
});
describe("file cells", () => {
	it("reads a cell's list of files and survives a cell that is not one", () => {
		expect(
			parseFileCell(
				'[{"id":"f1","name":"a.png","content_type":"image/png","url":"/api/x"},{"url":"https://example.com/b.pdf","name":"b.pdf"}]',
			),
		).toHaveLength(2);
		expect(parseFileCell("")).toEqual([]);
		expect(parseFileCell("not json")).toEqual([]);
		expect(parseFileCell('{"id":"f1"}')).toEqual([]);
	});

	it("writes an empty list as an empty cell", () => {
		expect(fileCellText([])).toBe("");
		expect(JSON.parse(fileCellText([{ url: "https://example.com/a.png", name: "a.png" }]))).toEqual([
			{ url: "https://example.com/a.png", name: "a.png" },
		]);
	});

	it("tells images from other files by type, and by extension when there is no type", () => {
		expect(isImageFile({ name: "a.bin", content_type: "image/png" })).toBe(true);
		expect(isImageFile({ name: "photo.JPG" })).toBe(true);
		expect(isImageFile({ name: "doc.pdf", content_type: "application/pdf" })).toBe(false);
		expect(isImageFile({ name: "vector.svg", content_type: "image/svg+xml" })).toBe(false);
	});

	it("lets a dataset have many file columns", () => {
		expect(columnTypesLeft(DATASET_COLUMN_TYPES, ["file", "input"])).toContain("file");
		expect(isStructuredColumn("file")).toBe(false);
	});
});