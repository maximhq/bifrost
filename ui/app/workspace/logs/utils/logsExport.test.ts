import type { LogEntry } from "@/lib/types/logs";
import { describe, expect, it } from "vitest";
import {
	LOGS_CSV_HEADERS,
	calculateExportTargetCount,
	formatActiveFiltersSummary,
	getLogApp,
	getLogCost,
	getLogTokenUsage,
	logEntryToCSVRow,
	logsToCSV,
	normalizeExportFilters,
} from "./logsExport";

describe("logsExport", () => {
	const mockLog: LogEntry = {
		id: "req-123",
		object: "chat.completion",
		timestamp: "2026-10-15T12:00:00Z",
		provider: "openai",
		model: "gpt-4o",
		canonical_model_name: "gpt-4o-2024-08-06",
		app: "web-ui",
		user_agent: "Mozilla/5.0",
		status: "success",
		stop_reason: "stop",
		latency: 1250,
		token_usage: {
			prompt_tokens: 100,
			completion_tokens: 50,
			total_tokens: 150,
		},
		cost: 0.0035,
		session_id: "sess-abc",
		service_tier: "priority",
		virtual_key_name: "prod-key",
		routing_rule_name: "rule-primary",
		team_name: "Engineering",
		customer_name: "Acme Corp",
		user_name: "Ada Lovelace",
		business_unit_name: "Platform",
		project_name: "Search V2",
		error_details: undefined,
		number_of_retries: 0,
		fallback_index: 0,
		stream: false,
		created_at: "2026-10-15T12:00:00Z",
		input_history: [],
		responses_input_history: [],
	};

	it("exports 23 predefined headers", () => {
		expect(LOGS_CSV_HEADERS).toHaveLength(23);
		expect(LOGS_CSV_HEADERS[0]).toBe("Time");
		expect(LOGS_CSV_HEADERS[1]).toBe("Request ID");
		expect(LOGS_CSV_HEADERS[12]).toBe("Cost ($)");
		expect(LOGS_CSV_HEADERS[22]).toBe("Stop Reason");
	});

	it("converts a complete LogEntry to CSV row cells", () => {
		const row = logEntryToCSVRow(mockLog);
		expect(row).toHaveLength(23);
		expect(row[0]).toBe("2026-10-15T12:00:00Z");
		expect(row[1]).toBe("req-123");
		expect(row[2]).toBe("success");
		expect(row[3]).toBe("chat.completion");
		expect(row[4]).toBe("openai");
		expect(row[5]).toBe("gpt-4o");
		expect(row[6]).toBe("gpt-4o-2024-08-06");
		expect(row[8]).toBe(1250);
		expect(row[9]).toBe(100);
		expect(row[10]).toBe(50);
		expect(row[11]).toBe(150);
		expect(row[12]).toBe(0.0035);
		expect(row[13]).toBe("sess-abc");
		expect(row[14]).toBe("priority");
		expect(row[15]).toBe("prod-key");
		expect(row[16]).toBe("rule-primary");
		expect(row[17]).toBe("Engineering");
		expect(row[18]).toBe("Acme Corp");
		expect(row[19]).toBe("Ada Lovelace");
		expect(row[20]).toBe("Platform");
		expect(row[21]).toBe("Search V2");
		expect(row[22]).toBe("stop");
	});

	it("handles missing optional values predictably", () => {
		const minimalLog: LogEntry = {
			id: "req-min",
			object: "text.completion",
			timestamp: "2026-10-15T12:00:00Z",
			provider: "anthropic",
			model: "claude-3-5-sonnet",
			status: "error",
			number_of_retries: 0,
			fallback_index: 0,
			stream: false,
			created_at: "2026-10-15T12:00:00Z",
			input_history: [],
			responses_input_history: [],
		};

		const row = logEntryToCSVRow(minimalLog);
		expect(row).toHaveLength(23);
		expect(row[2]).toBe("error");
		expect(row[8]).toBe(""); // latency
		expect(row[9]).toBe(""); // prompt tokens
		expect(row[10]).toBe(""); // completion tokens
		expect(row[11]).toBe(""); // total tokens
		expect(row[12]).toBe(""); // cost
		expect(row[13]).toBe(""); // session
		expect(row[15]).toBe(""); // virtual key
		expect(row[22]).toBe(""); // stop reason
	});

	it("normalizes export filters by converting period into fixed start_time and end_time", () => {
		const relativeFilters = {
			period: "1h",
			providers: ["openai"],
			status: ["success"],
		};
		const normalized = normalizeExportFilters(relativeFilters);
		expect(normalized.period).toBeUndefined();
		expect(normalized.providers).toEqual(["openai"]);
		expect(normalized.status).toEqual(["success"]);
		expect(typeof normalized.start_time).toBe("string");
		expect(typeof normalized.end_time).toBe("string");
		expect(new Date(normalized.start_time!).getTime()).toBeLessThan(new Date(normalized.end_time!).getTime());
	});

	it("preserves explicit start_time and end_time when period is absent", () => {
		const fixedFilters = {
			start_time: "2026-10-01T00:00:00.000Z",
			end_time: "2026-10-01T12:00:00.000Z",
			providers: ["anthropic"],
		};
		const normalized = normalizeExportFilters(fixedFilters);
		expect(normalized).toEqual(fixedFilters);
	});

	it("resolves multi-value attribution slices with semicolon separator", () => {
		const sliceLog: LogEntry = {
			...mockLog,
			team_name: undefined,
			team_names: ["Team A", "Team B"],
			customer_name: undefined,
			customer_names: ["Cust 1", "Cust 2"],
		};
		const row = logEntryToCSVRow(sliceLog);
		expect(row[17]).toBe("Team A; Team B");
		expect(row[18]).toBe("Cust 1; Cust 2");
	});

	it("extracts application display name from app or user-agent", () => {
		expect(getLogApp(mockLog)).toBe("web-ui");
		expect(getLogApp({ ...mockLog, app: undefined, user_agent: "Mozilla/5.0" })).toBeTruthy();
		expect(getLogApp({ ...mockLog, app: undefined, user_agent: undefined })).toBe("");
	});

	it("falls back to batch accounting for token usage when top-level token_usage is absent", () => {
		const batchLog: LogEntry = {
			...mockLog,
			cost: undefined,
			token_usage: undefined,
			batch_debug: {
				accounting: {
					cost: 0.12,
					model_breakdowns: {
						"item-1": {
							model: "gpt-4o",
							request_count: 1,
							usage: { prompt_tokens: 200, completion_tokens: 80, total_tokens: 280 },
						},
					},
				},
			},
		};
		const tokens = getLogTokenUsage(batchLog);
		expect(tokens.promptTokens).toBe(200);
		expect(tokens.completionTokens).toBe(80);
		expect(tokens.totalTokens).toBe(280);

		const cost = getLogCost(batchLog);
		expect(cost).toBe(0.12);
	});

	it("falls back to children_cost when cost is absent", () => {
		const asyncLog: LogEntry = {
			...mockLog,
			cost: undefined,
			children_cost: 0.045,
		};
		expect(getLogCost(asyncLog)).toBe(0.045);
	});

	it("formats active filters summary correctly", () => {
		const emptySummary = formatActiveFiltersSummary({});
		expect(emptySummary).toEqual([]);

		const fullSummary = formatActiveFiltersSummary({
			content_search: "error timeout",
			providers: ["openai", "anthropic"],
			status: ["error"],
			period: "24h",
		});
		expect(fullSummary).toContain('search "error timeout"');
		expect(fullSummary).toContain("providers (2)");
		expect(fullSummary).toContain("status: error");
		expect(fullSummary).toContain("period 24h");
	});

	it("calculates export target count correctly", () => {
		expect(calculateExportTargetCount(0)).toBe(0);
		expect(calculateExportTargetCount(2500)).toBe(2500);
		expect(calculateExportTargetCount(2500, 500)).toBe(500);
		expect(calculateExportTargetCount(15000)).toBe(10000);
		expect(calculateExportTargetCount(15000, 5000)).toBe(5000);
		expect(calculateExportTargetCount(15000, 20000)).toBe(10000);
		expect(calculateExportTargetCount(500, -10)).toBe(500);
	});

	it("builds a full CSV string with headers and escaped cell values", () => {
		const logs: LogEntry[] = [
			mockLog,
			{
				...mockLog,
				id: "req-injection",
				model: "=SUM(A1:B1)",
			},
		];
		const csv = logsToCSV(logs);
		const lines = csv.split("\n");

		expect(lines).toHaveLength(3);
		expect(lines[0]).toContain('"Time","Request ID","Status"');
		expect(lines[1]).toContain('"req-123"');
		// Formula injection in model should be sanitized with leading apostrophe
		expect(lines[2]).toContain('"\'=SUM(A1:B1)"');
	});
});