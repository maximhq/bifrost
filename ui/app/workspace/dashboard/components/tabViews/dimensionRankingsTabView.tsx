import { useGetDimensionRankingsQuery, useLazyGetDimensionRankingsQuery } from "@/lib/store";
import type { DimensionRankingsResponse, LogFilters, RankingDimension } from "@/lib/types/logs";
import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useState } from "react";
import type { DashboardData } from "../../utils/exportUtils";
import { DASHBOARD_RANKINGS_LIMIT } from "../../utils/rankings";
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
	/** While a PDF export is rendering, show the uncapped export snapshot instead of the capped view. */
	pdfMode?: boolean;
}

export const DimensionRankingsTabView = forwardRef<DimensionRankingsTabViewHandle, DimensionRankingsTabViewProps>(
	function DimensionRankingsTabView({ filters, active, dimension, dimensionLabel, testIdPrefix, dataKey, pdfMode }, ref) {
		const fetchArg = useMemo(() => ({ filters, dimension, limit: DASHBOARD_RANKINGS_LIMIT }), [filters, dimension]);
		// Exports are never truncated: they ask for every ranked entity.
		const exportArg = useMemo(() => ({ filters, dimension, all: true }), [filters, dimension]);
		const skipOpts = useMemo(() => ({ skip: !active }), [active]);

		const { data, isLoading: loading } = useGetDimensionRankingsQuery(fetchArg, skipOpts);

		const [triggerDimensionRankings] = useLazyGetDimensionRankingsQuery();
		const [exportData, setExportData] = useState<DimensionRankingsResponse | null>(null);

		// A snapshot belongs to the filters it was fetched with - drop it when those change.
		useEffect(() => setExportData(null), [exportArg]);

		const loadData = useCallback(async () => {
			try {
				setExportData(await triggerDimensionRankings(exportArg, true).unwrap());
			} catch {
				// Fall back to the capped view rather than failing the whole export.
				setExportData(null);
			}
		}, [exportArg, triggerDimensionRankings]);

		useImperativeHandle(
			ref,
			() => ({
				getData: () => ({ [dataKey]: exportData ?? data ?? null }),
				loadData,
			}),
			[data, exportData, dataKey, loadData],
		);

		return (
			<DimensionRankingsTab
				data={(pdfMode ? (exportData ?? data) : data) ?? null}
				loading={loading}
				dimensionLabel={dimensionLabel}
				testIdPrefix={testIdPrefix}
				attributed
			/>
		);
	},
);