import { describe, expect, it, vi } from "vitest";
import { buildCSV, downloadCSV, escapeCell } from "./csv";

describe("escapeCell", () => {
	it("handles null and undefined as empty quoted string", () => {
		expect(escapeCell(null)).toBe('""');
		expect(escapeCell(undefined)).toBe('""');
	});

	it("escapes double quotes by doubling them", () => {
		expect(escapeCell('hello "world"')).toBe('"hello ""world"""');
	});

	it("quotes strings containing commas or newlines", () => {
		expect(escapeCell("a,b")).toBe('"a,b"');
		expect(escapeCell("line1\nline2")).toBe('"line1\nline2"');
	});

	it("neutralizes formula injection characters (=, +, -, @, \\t, \\r)", () => {
		expect(escapeCell("=SUM(A1:B1)")).toBe('"\'=SUM(A1:B1)"');
		expect(escapeCell("@SUM(1,2)")).toBe('"\'@SUM(1,2)"');
		expect(escapeCell("+cmd|' /C calc'!A0")).toBe("\"'+cmd|' /C calc'!A0\"");
		expect(escapeCell("-calc")).toBe('"\'-calc"');
		expect(escapeCell("\tcmd")).toBe('"\'\tcmd"');
		expect(escapeCell("\rcmd")).toBe('"\'\rcmd"');
		expect(escapeCell("\t=cmd")).toBe('"\'\t=cmd"');
		expect(escapeCell("  =1+1")).toBe('"\'  =1+1"');
	});

	it("preserves legitimate negative and positive numbers without quoting them with single quotes", () => {
		expect(escapeCell(-42)).toBe('"-42"');
		expect(escapeCell("-42.5")).toBe('"-42.5"');
		expect(escapeCell("+100")).toBe('"+100"');
		expect(escapeCell(0)).toBe('"0"');
		expect(escapeCell(123.456)).toBe('"123.456"');
	});

	it("handles unicode and emojis safely", () => {
		expect(escapeCell("東京")).toBe('"東京"');
		expect(escapeCell("Café & résumé")).toBe('"Café & résumé"');
		expect(escapeCell("🚀")).toBe('"🚀"');
	});
});

describe("buildCSV", () => {
	it("builds a well-formatted CSV string from headers and rows", () => {
		const headers = ["ID", "Name", "Cost"];
		const rows = [
			["1", "Alice", 0.05],
			["2", 'Bob "The Builder", Jr.', 1.25],
			["3", "=cmd", null],
		];
		const result = buildCSV(headers, rows);
		const lines = result.split("\n");

		expect(lines).toHaveLength(4);
		expect(lines[0]).toBe('"ID","Name","Cost"');
		expect(lines[1]).toBe('"1","Alice","0.05"');
		expect(lines[2]).toBe('"2","Bob ""The Builder"", Jr.","1.25"');
		expect(lines[3]).toBe('"3","\'=cmd",""');
	});
});

describe("downloadCSV", () => {
	it("creates object url and triggers link click", () => {
		const mockClick = vi.fn();
		const mockRemove = vi.fn();
		const mockAppendChild = vi.fn();
		const mockBlob = vi.fn();

		const fakeElement = {
			set href(v: string) {},
			set download(v: string) {},
			click: mockClick,
			remove: mockRemove,
		};

		const globalScope = globalThis as unknown as Record<string, unknown>;
		const originalDocument = globalScope.document;
		const originalURL = globalThis.URL;
		const originalBlob = globalScope.Blob;

		globalScope.document = {
			createElement: vi.fn().mockReturnValue(fakeElement),
			body: {
				appendChild: mockAppendChild,
			},
		};

		globalScope.URL = {
			createObjectURL: vi.fn().mockReturnValue("blob:mock-url"),
			revokeObjectURL: vi.fn(),
		};

		globalScope.Blob = mockBlob;

		try {
			downloadCSV("test-content", "test-export");

			expect(mockBlob).toHaveBeenCalledWith(["\uFEFF", "test-content"], {
				type: "text/csv;charset=utf-8;",
			});
			expect(globalThis.URL.createObjectURL).toHaveBeenCalled();
			expect(mockAppendChild).toHaveBeenCalledWith(fakeElement);
			expect(mockClick).toHaveBeenCalled();
			expect(mockRemove).toHaveBeenCalled();
		} finally {
			globalScope.document = originalDocument;
			globalScope.URL = originalURL;
			globalScope.Blob = originalBlob;
		}
	});
});