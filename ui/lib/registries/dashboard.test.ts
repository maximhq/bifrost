import { describe, expect, it } from "vitest";
import { getOverviewCharts, registerOverviewChart } from "./dashboard";

const Chart = () => null;

describe("registerOverviewChart", () => {
	it("orders charts by order, then id, and replaces a chart with the same id", () => {
		registerOverviewChart({ id: "b", order: 20, component: Chart });
		registerOverviewChart({ id: "a", order: 10, component: Chart });
		registerOverviewChart({ id: "c", order: 10, component: Chart });
		registerOverviewChart({ id: "b", order: 5, component: Chart });
		expect(getOverviewCharts().map((c) => c.id)).toEqual(["b", "a", "c"]);
	});
});