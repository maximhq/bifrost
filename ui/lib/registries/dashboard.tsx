// Runtime registry for charts a downstream build adds to the dashboard's Overview tab.
//
// OSS registers nothing, so the Overview tab shows only its own charts. A downstream build
// registers a chart by importing its registration module via the @enterprise alias; see
// ui/app/_fallbacks/enterprise/lib/registrations/dashboard.ts for the OSS-build fallback.

import type { LogFilters } from "@/lib/types/logs";
import type { ComponentType } from "react";

export interface OverviewChartProps {
	// filters, startTime and endTime are the dashboard's current selection; active is false
	// while another tab is shown, so the chart can skip fetching.
	filters: LogFilters;
	startTime: number;
	endTime: number;
	active: boolean;
}

export interface OverviewChart {
	id: string;
	// order sorts registered charts; they render after the built-in charts.
	order: number;
	component: ComponentType<OverviewChartProps>;
}

let overviewCharts: OverviewChart[] = [];

/**
 * Registers (or replaces, by id) a chart drawn in the Overview tab's chart grid. The component
 * renders its own ChartCard. Intended to be called at module load, once, before the first render
 * that reads the registry.
 */
export function registerOverviewChart(chart: OverviewChart): void {
	overviewCharts = [...overviewCharts.filter((c) => c.id !== chart.id), chart].sort(
		(a, b) => a.order - b.order || a.id.localeCompare(b.id),
	);
}

/** Returns the registered Overview charts in order; empty in builds without any. */
export function getOverviewCharts(): readonly OverviewChart[] {
	return overviewCharts;
}