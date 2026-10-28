import { describe, expect, it } from "vitest";
import { buildRelayLogQuery, formatCostBound, parseCostBound } from "./relayLogQuery";

describe("buildRelayLogQuery", () => {
	it("repeats array filters, including apps, one value per param", () => {
		const params = new URLSearchParams(
			buildRelayLogQuery({
				apps: ["Cursor", "Claude Code"],
				user_ids: ["u1", ""],
				start_time: "2026-01-01T00:00:00Z",
				end_time: "2026-01-02T00:00:00Z",
			}),
		);
		expect(params.getAll("apps")).toEqual(["Cursor", "Claude Code"]);
		expect(params.getAll("user_ids")).toEqual(["u1"]);
		expect(params.get("start_time")).toBe("2026-01-01T00:00:00Z");
	});

	it("sends cost bounds, keeping zero and dropping unset or NaN", () => {
		const both = new URLSearchParams(buildRelayLogQuery({ min_cost: 0, max_cost: 1.5 }));
		expect(both.get("min_cost")).toBe("0");
		expect(both.get("max_cost")).toBe("1.5");

		const none = new URLSearchParams(buildRelayLogQuery({ min_cost: undefined, max_cost: Number.NaN }));
		expect(none.has("min_cost")).toBe(false);
		expect(none.has("max_cost")).toBe(false);
	});

	it("adds pagination and the billable flag", () => {
		const params = new URLSearchParams(buildRelayLogQuery({ billable: false }, { limit: 50, offset: 100, sort_by: "cost", order: "asc" }));
		expect(params.get("limit")).toBe("50");
		expect(params.get("offset")).toBe("100");
		expect(params.get("sort_by")).toBe("cost");
		expect(params.get("order")).toBe("asc");
		expect(params.get("billable")).toBe("false");
	});

	it("sends request_id and search as separate params", () => {
		const params = new URLSearchParams(buildRelayLogQuery({ request_id: "req-1", search: "/v1/audio" }));
		expect(params.get("request_id")).toBe("req-1");
		expect(params.get("search")).toBe("/v1/audio");
		expect(new URLSearchParams(buildRelayLogQuery({ search: "x" })).has("request_id")).toBe(false);
	});

	it("expands a relative period into an absolute window", () => {
		const params = new URLSearchParams(buildRelayLogQuery({ period: "1h" }));
		const from = new Date(params.get("start_time")!).getTime();
		const to = new Date(params.get("end_time")!).getTime();
		expect(to - from).toBeCloseTo(3600_000, -3);
	});
});

describe("cost bound inputs", () => {
	it("parses numbers, keeps zero and rejects blanks, text and negatives", () => {
		expect(parseCostBound("0")).toBe(0);
		expect(parseCostBound(" 0.25 ")).toBe(0.25);
		expect(parseCostBound("")).toBeUndefined();
		expect(parseCostBound("abc")).toBeUndefined();
		expect(parseCostBound("-1")).toBeUndefined();
	});

	it("formats an unset bound as an empty field", () => {
		expect(formatCostBound(undefined)).toBe("");
		expect(formatCostBound(0)).toBe("0");
		expect(formatCostBound(0.004)).toBe("0.004");
	});
});