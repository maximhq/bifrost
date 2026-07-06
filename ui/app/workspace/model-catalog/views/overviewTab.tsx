import FullPageLoader from "@/components/fullPageLoader";
import { ProviderNames } from "@/lib/constants/logs";
import { useGetModelsQuery, useGetProvidersQuery, useLazyGetLogsModelHistogramQuery, useLazyGetLogsStatsQuery } from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { LogStats } from "@/lib/types/logs";
import { useEffect, useMemo, useState } from "react";
import { ModelCatalogEmptyState } from "./modelCatalogEmptyState";
import ModelCatalogTable, { ModelCatalogRow } from "./modelCatalogTable";

interface OverviewTabProps {
	hasAccess: boolean;
}

export default function OverviewTab({ hasAccess }: OverviewTabProps) {
	const [providerFilter, setProviderFilter] = useState("");
	const [statsMap, setStatsMap] = useState<Map<string, LogStats>>(new Map());
	const [modelsUsedMap, setModelsUsedMap] = useState<Map<string, string[]>>(new Map());
	const [isLoadingModels, setIsLoadingModels] = useState(true);

	const {
		data: providers,
		isLoading: isLoadingProviders,
		error: providersError,
		refetch: refetchProviders,
	} = useGetProvidersQuery(undefined, { skip: !hasAccess });
	const { data: modelsData } = useGetModelsQuery({ unfiltered: true }, { skip: !hasAccess });

	const [triggerGlobalStats, { data: globalStats }] = useLazyGetLogsStatsQuery();
	const [triggerStats] = useLazyGetLogsStatsQuery();
	const [triggerModelHistogram] = useLazyGetLogsModelHistogramQuery();

	useEffect(() => {
		if (!hasAccess) return;
		const now = new Date().toISOString();
		const dayAgo = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
		triggerGlobalStats({ filters: { start_time: dayAgo, end_time: now } });
	}, [hasAccess, triggerGlobalStats]);

	useEffect(() => {
		if (!providers || providers.length === 0) return;
		let cancelled = false;
		const now = new Date().toISOString();
		const dayAgo = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();

		Promise.all(
			providers.map((p) =>
				triggerStats({ filters: { providers: [p.name], start_time: dayAgo, end_time: now } })
					.unwrap()
					.then((stats) => [p.name, stats] as const)
					.catch(
						() =>
							[
								p.name,
								{
									total_requests: 0,
									success_rate: 0,
									user_facing_success_rate: 0,
									average_latency: 0,
									user_facing_total_requests: 0,
									total_tokens: 0,
									total_cost: 0,
								},
							] as const,
					),
			),
		).then((results) => {
			if (!cancelled) setStatsMap(new Map(results));
		});
		return () => {
			cancelled = true;
		};
	}, [providers, triggerStats]);

	useEffect(() => {
		if (!providers || providers.length === 0) return;
		let cancelled = false;
		setIsLoadingModels(true);
		const now = new Date().toISOString();
		const monthAgo = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString();

		Promise.all(
			providers.map((p) =>
				triggerModelHistogram({ filters: { providers: [p.name], start_time: monthAgo, end_time: now } })
					.unwrap()
					.then((data): [string, string[]] => [p.name, data.models ?? []])
					.catch((): [string, string[]] => [p.name, []]),
			),
		).then((results) => {
			if (!cancelled) {
				setModelsUsedMap(new Map(results));
				setIsLoadingModels(false);
			}
		});
		return () => {
			cancelled = true;
		};
	}, [providers, triggerModelHistogram]);

	const rows: ModelCatalogRow[] = useMemo(() => {
		if (!providers) return [];
		return providers.map((p) => {
			const isCustom = !ProviderNames.includes(p.name as KnownProvider);
			const modelsUsed = modelsUsedMap.get(p.name) ?? [];
			const providerStats = statsMap.get(p.name);
			const totalTraffic24h = providerStats?.total_requests ?? 0;
			const totalCost24h = providerStats?.total_cost ?? 0;
			return {
				providerName: p.name,
				isCustom,
				baseProviderType: p.custom_provider_config?.base_provider_type,
				modelsUsed,
				totalTraffic24h,
				totalCost24h,
			};
		});
	}, [providers, statsMap, modelsUsedMap]);

	// Clear the provider filter if the selected provider is no longer in the
	// list (e.g. deleted in another tab / via gossip) — otherwise the table
	// silently shows zero rows with no way back except clicking the dropdown.
	useEffect(() => {
		if (!providerFilter || !providers) return;
		if (!providers.some((p) => p.name === providerFilter)) {
			setProviderFilter("");
		}
	}, [providers, providerFilter]);

	const filteredRows = useMemo(() => {
		if (!providerFilter) return rows;
		return rows.filter((r) => r.providerName === providerFilter);
	}, [rows, providerFilter]);

	if (isLoadingProviders) {
		return <FullPageLoader />;
	}

	if (providersError) {
		return (
			<div className="flex h-full flex-col items-center justify-center gap-4 text-center">
				<p className="text-muted-foreground text-sm">Failed to load providers</p>
				<button type="button" data-testid="model-catalog-retry-btn" onClick={refetchProviders} className="text-sm underline">
					Retry
				</button>
			</div>
		);
	}

	if (!providers || providers.length === 0) {
		return <ModelCatalogEmptyState />;
	}

	return (
		<ModelCatalogTable
			rows={filteredRows}
			providers={(providers ?? []).map((p) => p.name)}
			providerFilter={providerFilter}
			onProviderFilterChange={setProviderFilter}
			totalProviders={(providers ?? []).length}
			totalModels={modelsData?.total ?? 0}
			totalRequests24h={globalStats?.total_requests ?? 0}
			totalCost24h={globalStats?.total_cost ?? 0}
			isLoadingModels={isLoadingModels}
		/>
	);
}