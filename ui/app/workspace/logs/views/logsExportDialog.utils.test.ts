import type { LogEntry, LogFilters } from "@/lib/types/logs";
import { describe, expect, it, vi } from "vitest";
import {
	BATCH_PAGE_SIZE,
	DEFAULT_MAX_EXPORT_LIMIT,
	calculateExportTargetCount,
	collectLogsInBatches,
	formatActiveFiltersSummary,
	logsToCSV,
} from "../utils/logsExport";

describe("Logs Export Workflow & Pagination Logic", () => {
	const sampleLog: LogEntry = {
		id: "log-1",
		object: "chat.completion",
		timestamp: "2026-10-15T10:00:00Z",
		provider: "openai",
		model: "gpt-4o",
		status: "success",
		number_of_retries: 0,
		fallback_index: 0,
		stream: false,
		created_at: "2026-10-15T10:00:00Z",
		input_history: [],
		responses_input_history: [],
	};

	it("calculates correct batch slices for paginated fetching", () => {
		const targetTotal = calculateExportTargetCount(2500);
		expect(targetTotal).toBe(2500);

		const batches: { offset: number; limit: number }[] = [];
		let offset = 0;
		while (offset < targetTotal) {
			const batchLimit = Math.min(BATCH_PAGE_SIZE, targetTotal - offset);
			batches.push({ offset, limit: batchLimit });
			offset += batchLimit;
		}

		expect(batches).toEqual([
			{ offset: 0, limit: 1000 },
			{ offset: 1000, limit: 1000 },
			{ offset: 2000, limit: 500 },
		]);
	});

	it("caps batch requests at DEFAULT_MAX_EXPORT_LIMIT (10,000)", () => {
		const targetTotal = calculateExportTargetCount(50000);
		expect(targetTotal).toBe(DEFAULT_MAX_EXPORT_LIMIT);

		const batches: { offset: number; limit: number }[] = [];
		let offset = 0;
		while (offset < targetTotal) {
			const batchLimit = Math.min(BATCH_PAGE_SIZE, targetTotal - offset);
			batches.push({ offset, limit: batchLimit });
			offset += batchLimit;
		}

		expect(batches).toHaveLength(10);
		expect(batches[0]).toEqual({ offset: 0, limit: 1000 });
		expect(batches[9]).toEqual({ offset: 9000, limit: 1000 });
		expect(offset).toBe(10000);
	});

	it("handles user-configured caps lower and higher than default limit", () => {
		expect(calculateExportTargetCount(50000, 2500)).toBe(2500);
		// If user enters 25,000, it stays clamped at 10,000 max
		expect(calculateExportTargetCount(50000, 25000)).toBe(10000);
		// If total logs is fewer than cap, use total logs
		expect(calculateExportTargetCount(350, 1000)).toBe(350);
	});

	it("stops paginating immediately when cancellation flag is raised", async () => {
		let isCancelled = false;
		const mockFetch = vi.fn().mockImplementation(async (offset: number, limit: number) => {
			if (offset === 2000) {
				isCancelled = true;
			}
			return Array.from({ length: limit }, (_, i) => ({
				...sampleLog,
				id: `log-${offset + i}`,
			}));
		});

		const collected = await collectLogsInBatches({
			targetTotal: 5000,
			fetchBatch: mockFetch,
			isCancelled: () => isCancelled,
		});

		// Should have stopped after offset 2000 was fetched and cancelled
		expect(mockFetch).toHaveBeenCalledTimes(3);
		expect(collected).toHaveLength(2000);
	});

	it("stops paginating early when server returns fewer records than limit", async () => {
		const mockFetch = vi.fn().mockImplementation(async (offset: number, limit: number) => {
			// Second batch returns only 300 records instead of 1000
			const returnCount = offset === 1000 ? 300 : limit;
			return Array.from({ length: returnCount }, (_, i) => ({
				...sampleLog,
				id: `log-${offset + i}`,
			}));
		});

		const collected = await collectLogsInBatches({
			targetTotal: 5000,
			fetchBatch: mockFetch,
		});

		expect(mockFetch).toHaveBeenCalledTimes(2);
		expect(collected).toHaveLength(1300);
	});

	it("deduplicates records across batch boundaries by ID", async () => {
		const mockFetch = vi.fn().mockImplementation(async (offset: number, _limit: number) => {
			if (offset === 0) {
				return [
					{ ...sampleLog, id: "log-1" },
					{ ...sampleLog, id: "log-2" },
					{ ...sampleLog, id: "log-3" },
				];
			}
			if (offset === 3) {
				// Due to boundary shift, log-3 is returned again
				return [
					{ ...sampleLog, id: "log-3" },
					{ ...sampleLog, id: "log-4" },
					{ ...sampleLog, id: "log-5" },
				];
			}
			return [];
		});

		const collected = await collectLogsInBatches({
			targetTotal: 5,
			batchSize: 3,
			fetchBatch: mockFetch,
		});

		expect(collected.map((l) => l.id)).toEqual(["log-1", "log-2", "log-3", "log-4", "log-5"]);
	});

	it("preserves active filter dimensions in export queries", () => {
		const filters: LogFilters = {
			providers: ["openai", "bedrock"],
			models: ["gpt-4o", "claude-3-5-sonnet"],
			status: ["error"],
			content_search: "rate limit",
			session_id: "session-xyz",
			virtual_key_ids: ["vk-1"],
			team_ids: ["team-1"],
			customer_ids: ["cust-1"],
		};

		const summary = formatActiveFiltersSummary(filters);
		expect(summary).toContain('search "rate limit"');
		expect(summary).toContain("providers (2)");
		expect(summary).toContain("models (2)");
		expect(summary).toContain("status: error");
		expect(summary).toContain('session "session-xyz"');
		expect(summary).toContain("virtual keys (1)");
		expect(summary).toContain("teams (1)");
		expect(summary).toContain("customers (1)");
	});

	it("generates CSV without exposing prompt or response bodies", () => {
		const logWithSensitiveBodies: LogEntry = {
			...sampleLog,
			raw_request: "POST /v1/chat/completions Authorization: Bearer sk-secret",
			raw_response: '{"choices":[{"message":{"content":"confidential"}}]}',
			input_history: [{ role: "user", content: "Super sensitive prompt with passwords" }],
			output_message: { role: "assistant", content: "Confidential assistant response" },
		};

		const csv = logsToCSV([logWithSensitiveBodies]);

		// Verify metadata is exported
		expect(csv).toContain('"log-1"');
		expect(csv).toContain('"openai"');
		expect(csv).toContain('"gpt-4o"');

		// Verify sensitive prompt, response bodies, and credentials are NOT in CSV
		expect(csv).not.toContain("sk-secret");
		expect(csv).not.toContain("Super sensitive prompt with passwords");
		expect(csv).not.toContain("Confidential assistant response");
		expect(csv).not.toContain("Authorization: Bearer");
	});
});