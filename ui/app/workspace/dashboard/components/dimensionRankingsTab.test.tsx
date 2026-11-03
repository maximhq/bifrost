import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import i18n from "@/lib/i18n";
import type { DimensionRankingsResponse } from "@/lib/types/logs";
import { DimensionRankingsTab } from "./dimensionRankingsTab";

vi.mock("./charts/chartCard", () => ({
	ChartCard: ({ totalTooltip, secondaryTotalTooltip }: { totalTooltip?: ReactNode; secondaryTotalTooltip?: ReactNode }) =>
		createElement(
			"div",
			null,
			createElement("div", { "data-testid": "actual-tooltip" }, totalTooltip),
			createElement("div", { "data-testid": "attributed-tooltip" }, secondaryTotalTooltip),
		),
}));

const data: DimensionRankingsResponse = {
	dimension: "team",
	rankings: [
		{
			id: "team-a",
			name: "Team A",
			total_requests: 2,
			total_tokens: 20,
			total_cost: 0.1,
			trend: { has_previous_period: false, requests_trend: 0, tokens_trend: null, cost_trend: null },
		},
	],
	total_actual_requests: 1,
	total_attributed_requests: 2,
};

describe("dimension ranking request totals", () => {
	it("explains actual and attributed totals separately", async () => {
		await i18n.changeLanguage("en");
		const markup = renderToStaticMarkup(
			createElement(DimensionRankingsTab, { data, loading: false, dimensionLabel: "Team", testIdPrefix: "team", attributed: true }),
		);

		expect(markup).toContain("Actual number of requests sent.");
		expect(markup).toContain("A request counts toward each Team it belongs to");
	});
});