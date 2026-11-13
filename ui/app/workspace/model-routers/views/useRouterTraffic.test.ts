import type { DimensionRankingEntry } from "@/lib/types/logs";
import { describe, expect, test } from "vitest";
import { routerRequests, tierSplit, toRouterTraffic } from "./useRouterTraffic";

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
});