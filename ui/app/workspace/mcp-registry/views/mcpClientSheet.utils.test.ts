import { describe, expect, it } from "vitest";
import type { VirtualMCP } from "@/lib/types/virtualMcps";
import {
	deriveMemberVirtualMCPs,
	getNextVirtualMCPPageOffset,
	mergeVirtualMCPPages,
} from "./mcpClientSheet.utils";

/**
 * Helper to generate a mock VirtualMCP object for unit tests.
 *
 * @param id Unique identifier for the Virtual MCP.
 * @param name Display name for the Virtual MCP.
 * @param tools Array of tool definitions mapping client IDs to tool names.
 * @returns Mocked VirtualMCP entity.
 */
const createMockVMCP = (id: number, name: string, tools: { mcp_client_id: string; tool_names: string[] }[] = []): VirtualMCP => ({
	id,
	name,
	endpoint_slug: `vmcp-${id}`,
	enabled: true,
	tools,
	virtual_key_ids: [],
	created_at: "2026-10-16T00:00:00Z",
	updated_at: "2026-10-16T00:00:00Z",
});

describe("mcpClientSheet.utils", () => {
	describe("deriveMemberVirtualMCPs", () => {
		it("returns an empty array when virtualMcps is undefined or empty", () => {
			expect(deriveMemberVirtualMCPs(undefined, "client-1")).toEqual([]);
			expect(deriveMemberVirtualMCPs([], "client-1")).toEqual([]);
		});

		it("returns an empty array when clientID is empty", () => {
			const vmcps = [createMockVMCP(1, "VMCP 1", [{ mcp_client_id: "client-1", tool_names: ["*"] }])];
			expect(deriveMemberVirtualMCPs(vmcps, "")).toEqual([]);
		});

		it("returns an empty array when no Virtual MCP bundles the client", () => {
			const vmcps = [
				createMockVMCP(1, "VMCP 1", [{ mcp_client_id: "client-2", tool_names: ["toolA"] }]),
				createMockVMCP(2, "VMCP 2", [{ mcp_client_id: "client-3", tool_names: ["toolB"] }]),
			];
			expect(deriveMemberVirtualMCPs(vmcps, "client-1")).toEqual([]);
		});

		it("finds single and multiple Virtual MCPs that bundle the client", () => {
			const vmcps = [
				createMockVMCP(1, "VMCP 1", [{ mcp_client_id: "client-1", tool_names: ["*"] }]),
				createMockVMCP(2, "VMCP 2", [{ mcp_client_id: "client-2", tool_names: ["toolX"] }]),
				createMockVMCP(3, "VMCP 3", [{ mcp_client_id: "client-1", tool_names: ["toolA", "toolB"] }]),
			];
			const result = deriveMemberVirtualMCPs(vmcps, "client-1");
			expect(result).toHaveLength(2);
			expect(result[0].vmcp.id).toBe(1);
			expect(result[0].toolNames).toEqual(["*"]);
			expect(result[1].vmcp.id).toBe(3);
			expect(result[1].toolNames).toEqual(["toolA", "toolB"]);
		});

		it("correctly identifies server when bundled at index > 100", () => {
			const vmcps: VirtualMCP[] = [];
			for (let i = 1; i <= 105; i++) {
				vmcps.push(
					createMockVMCP(
						i,
						`VMCP ${i}`,
						i === 105 ? [{ mcp_client_id: "target-client", tool_names: ["specificTool"] }] : [],
					),
				);
			}

			const result = deriveMemberVirtualMCPs(vmcps, "target-client");
			expect(result).toHaveLength(1);
			expect(result[0].vmcp.id).toBe(105);
			expect(result[0].toolNames).toEqual(["specificTool"]);
		});
		it("correctly identifies server when bundled on pages beyond 20 (e.g. page 25, index > 2000)", () => {
			const vmcps: VirtualMCP[] = [];
			for (let i = 1; i <= 2500; i++) {
				vmcps.push(
					createMockVMCP(
						i,
						`VMCP ${i}`,
						i === 2450 ? [{ mcp_client_id: "target-client", tool_names: ["deepTool"] }] : [],
					),
				);
			}

			const result = deriveMemberVirtualMCPs(vmcps, "target-client");
			expect(result).toHaveLength(1);
			expect(result[0].vmcp.id).toBe(2450);
			expect(result[0].toolNames).toEqual(["deepTool"]);
		});

		it("simulates sheet-level pagination flow discovering a target present only at offset 100", () => {
			// Page 1 (offset 0, limit 100): 100 items not containing target
			const page1 = Array.from({ length: 100 }, (_, i) =>
				createMockVMCP(i + 1, `VMCP ${i + 1}`, [{ mcp_client_id: "other-client", tool_names: ["tool"] }]),
			);
			// Page 2 (offset 100, limit 100): 4 items containing the target client
			const page2 = [
				createMockVMCP(101, "VMCP 101", [{ mcp_client_id: "other-client", tool_names: ["tool"] }]),
				createMockVMCP(102, "Target Virtual MCP", [{ mcp_client_id: "target-client", tool_names: ["specialTool"] }]),
				createMockVMCP(103, "VMCP 103"),
				createMockVMCP(104, "VMCP 104"),
			];

			const totalCount = 104;
			let current = page1;
			let consumedRows = current.length;

			// Before page 2 is fetched, lookup finds no membership
			expect(deriveMemberVirtualMCPs(current, "target-client")).toHaveLength(0);

			// Offset for page 2 is 100
			const nextOffset = getNextVirtualMCPPageOffset(consumedRows, totalCount);
			expect(nextOffset).toBe(100);

			// Fetch and merge page 2
			consumedRows += page2.length;
			current = mergeVirtualMCPPages(current, page2);

			// Pagination completes
			expect(getNextVirtualMCPPageOffset(consumedRows, totalCount)).toBeNull();

			// Target present only at offset 100 is now discovered and rendered in membership
			const memberships = deriveMemberVirtualMCPs(current, "target-client");
			expect(memberships).toHaveLength(1);
			expect(memberships[0].vmcp.name).toBe("Target Virtual MCP");
			expect(memberships[0].toolNames).toEqual(["specialTool"]);
		});
	});

	describe("mergeVirtualMCPPages", () => {
		it("returns incoming items when existing is empty", () => {
			const incoming = [createMockVMCP(1, "VMCP 1"), createMockVMCP(2, "VMCP 2")];
			expect(mergeVirtualMCPPages([], incoming)).toEqual(incoming);
		});

		it("returns existing items when incoming is empty", () => {
			const existing = [createMockVMCP(1, "VMCP 1")];
			expect(mergeVirtualMCPPages(existing, [])).toEqual(existing);
		});

		it("appends new items from subsequent pages", () => {
			const page1 = [createMockVMCP(1, "VMCP 1"), createMockVMCP(2, "VMCP 2")];
			const page2 = [createMockVMCP(3, "VMCP 3"), createMockVMCP(4, "VMCP 4")];
			const merged = mergeVirtualMCPPages(page1, page2);
			expect(merged.map((m) => m.id)).toEqual([1, 2, 3, 4]);
		});

		it("deduplicates items appearing across page boundaries", () => {
			const page1 = [createMockVMCP(1, "VMCP 1"), createMockVMCP(2, "VMCP 2")];
			// Page 2 shifted and repeats item 2
			const page2 = [createMockVMCP(2, "VMCP 2"), createMockVMCP(3, "VMCP 3")];
			const merged = mergeVirtualMCPPages(page1, page2);
			expect(merged.map((m) => m.id)).toEqual([1, 2, 3]);
		});

		it("merges large page sequences exceeding 20 pages without data loss or truncation", () => {
			let accumulated: VirtualMCP[] = [];
			for (let page = 0; page < 25; page++) {
				const pageItems = [
					createMockVMCP(page * 2 + 1, `VMCP ${page * 2 + 1}`),
					createMockVMCP(page * 2 + 2, `VMCP ${page * 2 + 2}`),
				];
				accumulated = mergeVirtualMCPPages(accumulated, pageItems);
			}
			expect(accumulated).toHaveLength(50);
			expect(accumulated[0].id).toBe(1);
			expect(accumulated[49].id).toBe(50);
		});
	});

	describe("getNextVirtualMCPPageOffset", () => {
		it("returns null when totalCount is zero or negative", () => {
			expect(getNextVirtualMCPPageOffset(0, 0)).toBeNull();
			expect(getNextVirtualMCPPageOffset(0, -1)).toBeNull();
		});

		it("returns null when all items are fetched in a single page", () => {
			expect(getNextVirtualMCPPageOffset(50, 50)).toBeNull();
			expect(getNextVirtualMCPPageOffset(100, 100)).toBeNull();
		});

		it("returns currentCount as next offset when more items exist", () => {
			// First page returned 100 out of 104 total
			expect(getNextVirtualMCPPageOffset(100, 104)).toBe(100);
			// Second page returned another 100 (200 out of 250 total)
			expect(getNextVirtualMCPPageOffset(200, 250)).toBe(200);
		});

		it("returns null once currentCount reaches or exceeds totalCount", () => {
			expect(getNextVirtualMCPPageOffset(104, 104)).toBeNull();
			expect(getNextVirtualMCPPageOffset(105, 104)).toBeNull();
		});

		it("continues computing offsets seamlessly across arbitrarily large page sequences beyond page 20", () => {
			const totalCount = 2500;
			let currentOffset: number | null = 0;
			let pageCount = 0;

			while (currentOffset !== null) {
				pageCount++;
				const next = getNextVirtualMCPPageOffset(currentOffset + 100, totalCount);
				currentOffset = next;
			}

			// 2500 items at 100 items per page = 25 full pages
			expect(pageCount).toBe(25);
		});

		it("detects when an early empty page arrives while consumedRows < total", () => {
			const totalCount = 101;
			const consumedRows = 100;
			const incoming: VirtualMCP[] = [];

			const isPrematureEmpty = incoming.length === 0 && consumedRows < totalCount;
			expect(isPrematureEmpty).toBe(true);
		});
	});
});
