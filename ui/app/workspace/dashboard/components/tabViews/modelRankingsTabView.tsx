import {
	useGetLogsModelHistogramQuery,
	useGetModelRankingsQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetModelRankingsQuery,
} from "@/lib/store";
import type { LogFilters, ModelRankingsResponse } from "@/lib/types/logs";
import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useState } from "react";
import type { DashboardData } from "../../utils/exportUtils";
import { DASHBOARD_RANKINGS_LIMIT } from "../../utils/rankings";
import { ModelRankingsTab } from "../modelRankingsTab";

export interface ModelRankingsTabViewHandle {
	getData: () => Partial<DashboardData>;
	loadData: () => Promise<void>;
}

interface ModelRankingsTabViewProps {
	filters: LogFilters;
	active: boolean;
	startTime: number;
	endTime: number;
	/** While a PDF export is rendering, show the uncapped export snapshot instead of the capped view. */
	pdfMode?: boolean;
}

export const ModelRankingsTabView = forwardRef<ModelRankingsTabViewHandle, ModelRankingsTabViewProps>(function ModelRankingsTabView(
	{ filters, active, startTime, endTime, pdfMode },
	ref,
) {
	const fetchArg = useMemo(() => ({ filters }), [filters]);
	const rankingsArg = useMemo(() => ({ filters, limit: DASHBOARD_RANKINGS_LIMIT }), [filters]);
	// Exports are never truncated: they ask for every ranked model.
	const rankingsExportArg = useMemo(() => ({ filters, all: true }), [filters]);
	const skipOpts = useMemo(() => ({ skip: !active }), [active]);

	const { data: rankingsData, isLoading: loadingRankings } = useGetModelRankingsQuery(rankingsArg, skipOpts);
	const { data: modelData, isLoading: loadingModels } = useGetLogsModelHistogramQuery(fetchArg, skipOpts);

	const [triggerRankings] = useLazyGetModelRankingsQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();
	const [rankingsExportData, setRankingsExportData] = useState<ModelRankingsResponse | null>(null);

	// A snapshot belongs to the filters it was fetched with - drop it when those change.
	useEffect(() => setRankingsExportData(null), [rankingsExportArg]);

	const loadData = useCallback(async () => {
		const [rankings] = await Promise.all([
			// Fall back to the capped view rather than failing the whole export.
			triggerRankings(rankingsExportArg, true)
				.unwrap()
				.catch(() => null),
			triggerModels(fetchArg, true),
		]);
		setRankingsExportData(rankings);
	}, [fetchArg, rankingsExportArg, triggerRankings, triggerModels]);

	useImperativeHandle(
		ref,
		() => ({
			getData: () => ({
				rankingsData: rankingsExportData ?? rankingsData ?? null,
				modelData: modelData ?? null,
			}),
			loadData,
		}),
		[rankingsData, rankingsExportData, modelData, loadData],
	);

	return (
		<ModelRankingsTab
			rankingsData={(pdfMode ? (rankingsExportData ?? rankingsData) : rankingsData) ?? null}
			loading={loadingRankings}
			modelData={modelData ?? null}
			loadingModels={loadingModels}
			startTime={startTime}
			endTime={endTime}
		/>
	);
});