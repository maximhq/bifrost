import type { DimensionRankingEntry } from "@/lib/types/logs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, test, vi } from "vitest";
import {
	customRangeLabel,
	routerRequests,
	tierSplit,
	toRouterTraffic,
	trafficLogsSearch,
	trafficSelection,
	type TrafficWindow,
	trafficWindow,
	useRouterTierSplit,
	useRouterTraffic,
} from "./useRouterTraffic";

const mocks = vi.hoisted(() => ({
	// Stands in for the rankings read: called with the query argument, answers with the query's state.
	rankings: vi.fn(),
}));

vi.mock("@/lib/store/apis/logsApi", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/store/apis/logsApi")>()),
	useGetDimensionRankingsQuery: (arg: unknown) => mocks.rankings(arg),
}));

// readHook runs a hook through one render and returns what it gave.
function readHook<T>(hook: () => T): T {
	let value: { current: T } | undefined;
	const Probe = () => {
		value = { current: hook() };
		return null;
	};
	renderToStaticMarkup(createElement(Probe));
	if (!value) throw new Error("the hook did not run");
	return value.current;
}

const entry = (overrides: Partial<DimensionRankingEntry> = {}): DimensionRankingEntry => ({
	id: "support",
	total_requests: 17,
	total_tokens: 0,
	total_cost: 0.42,
	trend: { has_previous_period: true, requests_trend: 6, tokens_trend: null, cost_trend: 18 },
	...overrides,
});

describe("toRouterTraffic", () => {
	test("keeps the cost change when the router took requests the week before", () => {
		expect(toRouterTraffic(entry())).toEqual({ requests: 17, cost: 0.42, costTrend: 18 });
	});

	test("has no cost change without a week before", () => {
		const first = entry({ trend: { has_previous_period: false, requests_trend: 0, tokens_trend: null, cost_trend: 0 } });
		expect(toRouterTraffic(first).costTrend).toBeNull();
	});

	test("has no cost change when the week before cost nothing", () => {
		const free = entry({ trend: { has_previous_period: true, requests_trend: 6, tokens_trend: null, cost_trend: null } });
		expect(toRouterTraffic(free).costTrend).toBeNull();
	});

	test("reads a row without a trend", () => {
		expect(toRouterTraffic(entry({ trend: undefined as unknown as DimensionRankingEntry["trend"] })).costTrend).toBeNull();
	});

	test("keeps a week that cost the same as the one before as no change, not as nothing to compare", () => {
		const flat = entry({ trend: { has_previous_period: true, requests_trend: 0, tokens_trend: 0, cost_trend: 0 } });
		expect(toRouterTraffic(flat).costTrend).toBe(0);
	});

	test("keeps a fall in cost as a negative change", () => {
		const cheaper = entry({ trend: { has_previous_period: true, requests_trend: -10, tokens_trend: null, cost_trend: -42.5 } });
		expect(toRouterTraffic(cheaper).costTrend).toBe(-42.5);
	});

	test("ignores a cost change sent for a router with no week before", () => {
		const first = entry({ trend: { has_previous_period: false, requests_trend: 100, tokens_trend: null, cost_trend: 100 } });
		expect(toRouterTraffic(first).costTrend).toBeNull();
	});

	test("reads a router that took nothing this week after a busy one", () => {
		const quiet = entry({
			total_requests: 0,
			total_cost: 0,
			trend: { has_previous_period: true, requests_trend: -100, tokens_trend: null, cost_trend: -100 },
		});
		expect(toRouterTraffic(quiet)).toEqual({ requests: 0, cost: 0, costTrend: -100 });
	});
});

describe("tierSplit", () => {
	test("orders the tiers Simple to Complex, with a zero for a tier that took nothing", () => {
		const rankings = [entry({ id: "COMPLEX", total_requests: 1 }), entry({ id: "SIMPLE", total_requests: 12 })];
		expect(tierSplit(rankings)).toEqual([
			{ tier: "SIMPLE", requests: 12 },
			{ tier: "MEDIUM", requests: 0 },
			{ tier: "COMPLEX", requests: 1 },
		]);
	});

	test("leaves out a tier the router no longer has", () => {
		const rankings = [entry({ id: "REASONING", total_requests: 5 }), entry({ id: "MEDIUM", total_requests: 4 })];
		expect(tierSplit(rankings).map((count) => count.requests)).toEqual([0, 4, 0]);
	});

	test("shows all three tiers at zero for a router that took nothing", () => {
		expect(tierSplit([])).toEqual([
			{ tier: "SIMPLE", requests: 0 },
			{ tier: "MEDIUM", requests: 0 },
			{ tier: "COMPLEX", requests: 0 },
		]);
	});

	test("shows a router whose requests all fell in one tier", () => {
		expect(tierSplit([entry({ id: "MEDIUM", total_requests: 9 })]).map((count) => count.requests)).toEqual([0, 9, 0]);
	});
});

describe("routerRequests", () => {
	const loaded = new Map([["support", { requests: 12, cost: 1, costTrend: null }]]);

	test("shows nothing until the read has answered", () => {
		expect(routerRequests(undefined, "support")).toBeUndefined();
	});

	test("shows a router's count once loaded", () => {
		expect(routerRequests(loaded, "support")).toBe(12);
	});

	test("shows zero for a router the loaded read does not list", () => {
		expect(routerRequests(loaded, "billing")).toBe(0);
	});

	test("shows zero for a router that took no requests", () => {
		expect(routerRequests(new Map([["support", { requests: 0, cost: 0, costTrend: null }]]), "support")).toBe(0);
	});

	test("shows zero for every router when the read answered with none", () => {
		expect(routerRequests(new Map(), "support")).toBe(0);
	});
});

describe("trafficSelection", () => {
	test("is the last seven days when the URL names no period", () => {
		expect(trafficSelection(null, null, null)).toEqual({ period: "7d" });
	});

	test("takes a preset the picker offers", () => {
		expect(trafficSelection("30d", null, null)).toEqual({ period: "30d" });
		expect(trafficSelection("this_month", null, null)).toEqual({ period: "this_month" });
	});

	test("prefers a preset over a range left in the URL", () => {
		expect(trafficSelection("24h", 100, 200)).toEqual({ period: "24h" });
	});

	test("takes a custom range with both ends in order", () => {
		expect(trafficSelection(null, 1772541000, 1773145800)).toEqual({ start: 1772541000, end: 1773145800 });
	});

	test("falls back to the default for a period it does not know or a range it cannot use", () => {
		expect(trafficSelection("90d", null, null)).toEqual({ period: "7d" });
		expect(trafficSelection(null, 1773145800, null)).toEqual({ period: "7d" });
		expect(trafficSelection(null, 1773145800, 1772541000)).toEqual({ period: "7d" });
	});
});

describe("trafficWindow", () => {
	test("sends a preset by name, for the gateway to resolve", () => {
		expect(trafficWindow({ period: "30d" })).toEqual({ period: "30d" });
	});

	test("sends a custom range as its two ends", () => {
		expect(trafficWindow({ start: 1772541000, end: 1773145800 })).toEqual({
			start_time: "2026-03-03T12:30:00.000Z",
			end_time: "2026-03-10T12:30:00.000Z",
		});
	});
});

describe("trafficLogsSearch", () => {
	test("opens the logs over the same preset or the same range", () => {
		expect(trafficLogsSearch({ period: "30d" })).toEqual({ period: "30d" });
		expect(trafficLogsSearch({ start: 1772541000, end: 1773145800 })).toEqual({ start_time: 1772541000, end_time: 1773145800 });
	});
});

describe("customRangeLabel", () => {
	// The formatter sets its dash between thin spaces; the label is compared with plain ones.
	const plain = (label: string) => label.replace(/\s+/g, " ");

	test("names a range by its two dates", () => {
		const label = customRangeLabel(Date.UTC(2026, 2, 3, 12) / 1000, Date.UTC(2026, 2, 10, 12) / 1000, "en", "UTC");
		expect(plain(label)).toBe("Mar 3 – 10, 2026");
	});

	test("reads the dates in the picker's timezone, so a range across its midnight names both days", () => {
		// 23:30 on March 3 to 00:30 on March 4 in Los Angeles, which is all March 4 in UTC.
		const start = Date.UTC(2026, 2, 4, 7, 30) / 1000;
		const end = Date.UTC(2026, 2, 4, 8, 30) / 1000;
		expect(plain(customRangeLabel(start, end, "en", "America/Los_Angeles"))).toBe("Mar 3 – 4, 2026");
		expect(plain(customRangeLabel(start, end, "en", "UTC"))).toBe("Mar 4, 2026");
	});
});

describe("useRouterTraffic", () => {
	const window: TrafficWindow = { start_time: "2026-03-03T12:30:00.000Z", end_time: "2026-03-10T12:30:00.000Z" };
	const answered = (rankings: DimensionRankingEntry[] | null) => ({ data: { rankings, dimension: "model_router" }, isLoading: false });

	beforeEach(() => {
		mocks.rankings.mockReset();
	});

	test("asks once for every router's traffic over the window, not a top few", () => {
		mocks.rankings.mockReturnValue(answered([]));
		readHook(() => useRouterTraffic(window));
		expect(mocks.rankings).toHaveBeenCalledTimes(1);
		expect(mocks.rankings).toHaveBeenCalledWith({ filters: window, dimension: "model_router", all: true });
	});

	test("has no figures while the first read is in flight", () => {
		mocks.rankings.mockReturnValue({ data: undefined, isLoading: true });
		const read = readHook(() => useRouterTraffic(window));
		expect(read).toEqual({ byRouter: undefined, state: "pending" });
		expect(routerRequests(read.byRouter, "support"), "the list shows no count yet").toBeUndefined();
	});

	test("has no figures, and says so, when the read failed with nothing to show", () => {
		mocks.rankings.mockReturnValue({ data: undefined, isLoading: false, error: { status: 500 } });
		expect(readHook(() => useRouterTraffic(window))).toEqual({ byRouter: undefined, state: "unavailable" });
	});

	test("keys each router's figures by its id", () => {
		mocks.rankings.mockReturnValue(
			answered([
				entry({ id: "support", total_requests: 17, total_cost: 0.42 }),
				entry({ id: "billing", total_requests: 3, total_cost: 0.05, trend: undefined as unknown as DimensionRankingEntry["trend"] }),
			]),
		);
		const read = readHook(() => useRouterTraffic(window));
		expect(read.state).toBe("ready");
		expect(Object.fromEntries(read.byRouter ?? [])).toEqual({
			support: { requests: 17, cost: 0.42, costTrend: 18 },
			billing: { requests: 3, cost: 0.05, costTrend: null },
		});
	});

	test("is ready with no figures when no router took a request, so each shows zero", () => {
		mocks.rankings.mockReturnValue(answered([]));
		const read = readHook(() => useRouterTraffic(window));
		expect(read.state).toBe("ready");
		expect(read.byRouter?.size).toBe(0);
		expect(routerRequests(read.byRouter, "support")).toBe(0);
	});

	test("reads an answer that lists no rankings at all as no traffic", () => {
		// The gateway sends null for an empty list.
		mocks.rankings.mockReturnValue(answered(null));
		const read = readHook(() => useRouterTraffic(window));
		expect(read.state).toBe("ready");
		expect(read.byRouter?.size).toBe(0);
	});

	test("keeps the last figures when a later read fails", () => {
		mocks.rankings.mockReturnValue({ ...answered([entry({ id: "support", total_requests: 17 })]), error: { status: 500 } });
		const read = readHook(() => useRouterTraffic(window));
		expect(read.state).toBe("ready");
		expect(routerRequests(read.byRouter, "support")).toBe(17);
	});
});

describe("useRouterTierSplit", () => {
	const window: TrafficWindow = { start_time: "2026-03-03T12:30:00.000Z", end_time: "2026-03-10T12:30:00.000Z" };

	beforeEach(() => {
		mocks.rankings.mockReset();
	});

	test("asks for the tiers of this router alone, over the same window", () => {
		mocks.rankings.mockReturnValue({ data: undefined, isLoading: true });
		readHook(() => useRouterTierSplit("support", window));
		expect(mocks.rankings).toHaveBeenCalledWith({
			filters: { ...window, model_router_ids: ["support"] },
			dimension: "complexity_tier",
			all: true,
		});
	});

	test("has no split until the read answers", () => {
		mocks.rankings.mockReturnValue({ data: undefined, isLoading: true });
		expect(readHook(() => useRouterTierSplit("support", window))).toBeUndefined();
		mocks.rankings.mockReturnValue({ data: undefined, isLoading: false, error: { status: 500 } });
		expect(readHook(() => useRouterTierSplit("support", window))).toBeUndefined();
	});

	test("splits the router's requests across the three tiers", () => {
		mocks.rankings.mockReturnValue({
			data: {
				rankings: [entry({ id: "COMPLEX", total_requests: 2 }), entry({ id: "SIMPLE", total_requests: 15 })],
				dimension: "complexity_tier",
			},
		});
		expect(readHook(() => useRouterTierSplit("support", window))).toEqual([
			{ tier: "SIMPLE", requests: 15 },
			{ tier: "MEDIUM", requests: 0 },
			{ tier: "COMPLEX", requests: 2 },
		]);
	});

	test("shows every tier at zero when the router took nothing", () => {
		mocks.rankings.mockReturnValue({ data: { rankings: null, dimension: "complexity_tier" } });
		expect(readHook(() => useRouterTierSplit("support", window))?.map((count) => count.requests)).toEqual([0, 0, 0]);
	});
});