import { describe, expect, it } from "vitest";
import type { VirtualMCP } from "@/lib/types/virtualMcps";
import {
	deriveMemberVirtualMCPs,
	getNextVirtualMCPPageOffset,
	mergeVirtualMCPPages,
} from "./mcpClientSheet.utils";

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
	});
});
