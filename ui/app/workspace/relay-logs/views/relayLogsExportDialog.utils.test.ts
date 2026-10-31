import { collectLogsInBatches } from "@/app/workspace/logs/utils/logsExport";
import type { RelayLog } from "@/lib/types/relays";
import { describe, expect, it, vi } from "vitest";
import {
	formatRelayActiveFiltersSummary,
	normalizeRelayExportFilters,
	RELAY_LOGS_CSV_HEADERS,
	relayLogsToCSV,
	relayLogToCSVRow,
} from "./relayLogsExportDialog.utils";

const sampleLog: RelayLog = {
	id: "log-1",
	timestamp: "2026-10-15T10:00:00Z",
	relay_name: "voice",
	kind: "call",
	method: "POST",
	path: "/v1/tts",
	route_path: "/v1/tts",
	status: "success",
	status_code: 200,
	streamed: false,
	billable: true,
	model: "eleven_v3",
	latency: 120,
	upstream_latency: 100,
	overhead_latency: 20,
	bytes_out: 2048,
	cost: 0.0125,
	cost_per_unit: 0.0001,
	units: 125,
	billed_units: 125,
	user_agent: "curl/8.0",
	virtual_key_id: "vk-1",
	virtual_key_name: "prod",
	team_id: "team-1",
	request_id: "0b1f7a6e-2b1c-4f8e-9d6a-1f2e3d4c5b6a",
	request_headers: { authorization: "Bearer sk-secret" },
	request_body: '{"text":"super secret prompt"}',
	response_body: '{"audio":"confidential"}',
	content_hidden: false,
};

describe("relayLogsToCSV", () => {
	it("writes one cell per header, in header order", () => {
		const row = relayLogToCSVRow(sampleLog);
		// Same leading columns as the LLM logs export, so the two files line up side by side.
		expect(RELAY_LOGS_CSV_HEADERS.slice(0, 3)).toEqual(["Time", "Request ID", "Status"]);
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Request ID")]).toBe(sampleLog.request_id);
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Cost ($)")]).toBe(0.0125);
		expect(row).toHaveLength(RELAY_LOGS_CSV_HEADERS.length);
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Relay")]).toBe("voice");
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Status")]).toBe("success");
		// The HTTP code stays in the detail sheet; the export carries the status only, as the LLM export does.
		expect(RELAY_LOGS_CSV_HEADERS).not.toContain("Status Code");
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Billable")]).toBe("true");
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Streamed")]).toBe("false");
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Virtual Key")]).toBe("prod");
		// Falls back to the id when no name is on the row.
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Team")]).toBe("team-1");
		// Absent optional numbers are empty cells, not "undefined".
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Input Tokens")]).toBe("");
		expect(row[RELAY_LOGS_CSV_HEADERS.indexOf("Job ID")]).toBe("");
	});

	it("starts with the header row and never includes bodies or headers", () => {
		const csv = relayLogsToCSV([sampleLog]);
		const [header, first] = csv.split("\n");
		expect(header).toBe(RELAY_LOGS_CSV_HEADERS.map((h) => `"${h}"`).join(","));
		expect(first).toContain('"voice"');
		expect(first).toContain('"0b1f7a6e-2b1c-4f8e-9d6a-1f2e3d4c5b6a"');
		expect(csv).not.toContain("sk-secret");
		expect(csv).not.toContain("super secret prompt");
		expect(csv).not.toContain("confidential");
	});

	it("batches relay rows with the shared collector and deduplicates across boundaries", async () => {
		const fetchBatch = vi.fn().mockImplementation(async (offset: number) => {
			if (offset === 0)
				return [
					{ ...sampleLog, id: "a" },
					{ ...sampleLog, id: "b" },
				];
			if (offset === 2)
				return [
					{ ...sampleLog, id: "b" },
					{ ...sampleLog, id: "c" },
				];
			return [];
		});
		const collected = await collectLogsInBatches<RelayLog>({ targetTotal: 3, batchSize: 2, fetchBatch });
		expect(collected.map((l) => l.id)).toEqual(["a", "b", "c"]);
	});
});

describe("formatRelayActiveFiltersSummary", () => {
	it("names every filter in force", () => {
		const summary = formatRelayActiveFiltersSummary({
			request_id: "req-1",
			search: "tts",
			relay_names: ["voice", "search"],
			statuses: ["error"],
			billable: false,
			min_cost: 0,
			max_cost: 2,
			period: "1h",
		});
		expect(summary).toEqual([
			'request "req-1"',
			'search "tts"',
			"relays (2)",
			"status: error",
			"non-billable only",
			"min cost 0",
			"max cost 2",
			"period 1h",
		]);
	});

	it("is empty with no filters", () => {
		expect(formatRelayActiveFiltersSummary({})).toEqual([]);
	});
});

describe("normalizeRelayExportFilters", () => {
	it("pins a relative period to an absolute window and leaves fixed windows alone", () => {
		const pinned = normalizeRelayExportFilters({ period: "1h", relay_names: ["voice"] });
		expect(pinned.period).toBeUndefined();
		expect(pinned.relay_names).toEqual(["voice"]);
		const span = new Date(pinned.end_time!).getTime() - new Date(pinned.start_time!).getTime();
		expect(span).toBeCloseTo(3600_000, -3);

		const fixed = { start_time: "2026-01-01T00:00:00Z", end_time: "2026-01-02T00:00:00Z" };
		expect(normalizeRelayExportFilters(fixed)).toBe(fixed);
	});
});