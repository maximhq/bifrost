import {
	useGetLogsModelHistogramQuery,
	useGetModelRankingsQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetModelRankingsQuery,
} from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { forwardRef, useCallback, useImperativeHandle, useMemo } from "react";
import type { DashboardData } from "../../utils/exportUtils";
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
}

export const ModelRankingsTabView = forwardRef<ModelRankingsTabViewHandle, ModelRankingsTabViewProps>(function ModelRankingsTabView(
	{ filters, active, startTime, endTime },
	ref,
) {
	const fetchArg = useMemo(() => ({ filters }), [filters]);
	const skipOpts = useMemo(() => ({ skip: !active }), [active]);

	const { data: rankingsData, isLoading: loadingRankings } = useGetModelRankingsQuery(fetchArg, skipOpts);
	const { data: modelData, isLoading: loadingModels } = useGetLogsModelHistogramQuery(fetchArg, skipOpts);

	const [triggerRankings] = useLazyGetModelRankingsQuery();
	const [triggerModels] = useLazyGetLogsModelHistogramQuery();

	const loadData = useCallback(async () => {
		await Promise.all([triggerRankings(fetchArg, true), triggerModels(fetchArg, true)]);
	}, [fetchArg, triggerRankings, triggerModels]);

	useImperativeHandle(
		ref,
		() => ({
			getData: () => ({
				rankingsData: rankingsData ?? null,
				modelData: modelData ?? null,
			}),
			loadData,
		}),
		[rankingsData, modelData, loadData],
	);

	return (
		<ModelRankingsTab
			rankingsData={rankingsData ?? null}
			loading={loadingRankings}
			modelData={modelData ?? null}
			loadingModels={loadingModels}
			startTime={startTime}
			endTime={endTime}
		/>
	);
});