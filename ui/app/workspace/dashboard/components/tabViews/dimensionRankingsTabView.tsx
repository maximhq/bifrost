import { useGetDimensionRankingsQuery, useLazyGetDimensionRankingsQuery } from "@/lib/store";
import type { DimensionRankingsResponse, LogFilters, RankingDimension } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
import type { DashboardData } from "../../utils/exportUtils";
import { DimensionRankingsTab } from "../dimensionRankingsTab";

export interface DimensionRankingsTabViewHandle {
	getData: () => Partial<DashboardData>;
	loadData: () => Promise<void>;
}

interface DimensionRankingsTabViewProps {
	filters: LogFilters;
	active: boolean;
	dimension: RankingDimension;
	dimensionLabel: string;
	testIdPrefix: string;
	dataKey: keyof DashboardData;
	// Optional client-side reshape of the response before rendering/export. Used by
	// the App tab to roll per-version User-Agent rows up into one row per client app.
	transform?: (data: DimensionRankingsResponse | null) => DimensionRankingsResponse | null;
}

export const DimensionRankingsTabView = forwardRef<DimensionRankingsTabViewHandle, DimensionRankingsTabViewProps>(
	function DimensionRankingsTabView({ filters, active, dimension, dimensionLabel, testIdPrefix, dataKey, transform }, ref) {
		const fetchArg = useMemo(() => ({ filters, dimension }), [filters, dimension]);
		const skipOpts = useMemo(() => ({ skip: !active }), [active]);

		const { data, isLoading: loading } = useGetDimensionRankingsQuery(fetchArg, skipOpts);

		const [triggerDimensionRankings] = useLazyGetDimensionRankingsQuery();

		const displayData = useMemo(() => (transform ? transform(data ?? null) : (data ?? null)), [data, transform]);

		const loadData = useCallback(async () => {
			await triggerDimensionRankings(fetchArg, true);
		}, [fetchArg, triggerDimensionRankings]);

		useImperativeHandle(
			ref,
			() => ({
				getData: () => ({ [dataKey]: displayData }),
				loadData,
			}),
			[displayData, dataKey, loadData],
		);

		return (
			<DimensionRankingsTab
				data={displayData}
				loading={loading}
				dimensionLabel={dimensionLabel}
				testIdPrefix={testIdPrefix}
				attributed
			/>
		);
	},
);