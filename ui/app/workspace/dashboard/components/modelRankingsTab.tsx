import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import ProviderIcons, { type ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import type { ModelHistogramResponse, ModelRankingEntry, ModelRankingsResponse } from "@/lib/types/logs";
import { formatCompactNumber as formatNumber } from "@/lib/utils/governance";
import { ArrowDown, ArrowUp, ArrowUpDown, Minus } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatFullTimestamp, formatTimestamp, getModelColor } from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { ChartErrorBoundary } from "./charts/chartErrorBoundary";

type SortField = "total_requests" | "success_rate" | "total_tokens" | "total_cost" | "avg_latency";
type SortOrder = "asc" | "desc";

interface ModelRankingsTabProps {
	rankingsData: ModelRankingsResponse | null;
	loading: boolean;
	modelData: ModelHistogramResponse | null;
	loadingModels: boolean;
	startTime: number;
	endTime: number;
}

function formatCost(value: number): string {
	if (value >= 1) return `$${value.toFixed(2)}`;
	if (value >= 0.01) return `$${value.toFixed(3)}`;
	if (value > 0) return `$${value.toFixed(4)}`;
	return "$0.00";
}

function formatLatency(ms: number): string {
	if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
	return `${ms.toFixed(0)}ms`;
}

function TrendBadge({ value, positiveIsGood = true, isNew = false }: { value: number; positiveIsGood?: boolean; isNew?: boolean }) {
	if (isNew) {
		return <span className="inline-flex items-center gap-0.5 text-xs font-medium text-blue-600 dark:text-blue-400">new</span>;
	}

	if (value === 0) {
		return (
			<span className="text-muted-foreground inline-flex items-center gap-0.5 text-xs">
				<Minus className="h-3 w-3" />
			</span>
		);
	}

	const isPositive = value > 0;
	const isGood = positiveIsGood ? isPositive : !isPositive;
	return (
		<span
			className={`inline-flex items-center gap-0.5 text-xs font-medium ${isGood ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400"}`}
		>
			{isPositive ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />}
			{Math.abs(value).toFixed(1)}%
		</span>
	);
}

function SortableHeader({
	label,
	field,
	currentSort,
	currentOrder,
	onSort,
}: {
	label: string;
	field: SortField;
	currentSort: SortField;
	currentOrder: SortOrder;
	onSort: (field: SortField) => void;
}) {
	const isActive = currentSort === field;
	const ariaSort = isActive ? (currentOrder === "asc" ? "ascending" : "descending") : "none";
	return (
		<button
			type="button"
			data-testid={`sort-${field}-btn`}
			aria-sort={ariaSort}
			className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
			onClick={() => onSort(field)}
		>
			{label}
			{isActive ? (
				currentOrder === "desc" ? (
					<ArrowDown className="h-3 w-3" />
				) : (
					<ArrowUp className="h-3 w-3" />
				)
			) : (
				<ArrowUpDown className="text-muted-foreground h-3 w-3" />
			)}
		</button>
	);
}

// Tooltip for the usage share chart
function UsageShareTooltip({ active, payload, models }: any) {
	if (!active || !payload || !payload.length) return null;
	const data = payload[0]?.payload;
	if (!data) return null;

	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{formatFullTimestamp(data.timestamp)}</div>
			<div className="space-y-1 text-sm">
				{models.map((model: string, idx: number) => {
					const val = data[`model_${idx}`];
					if (!val || val === 0) return null;
					return (
						<div key={model} className="flex items-center justify-between gap-4">
							<span className="flex items-center gap-1.5">
								<span className="h-2 w-2 rounded-full" style={{ backgroundColor: getModelColor(idx) }} />
								<span className="max-w-[140px] truncate text-zinc-600 dark:text-zinc-400">{model}</span>
							</span>
							<span className="font-medium">{val.toLocaleString()}</span>
						</div>
					);
				})}
			</div>
		</div>
	);
}

// Top Models usage share stacked area chart + ranked legend
function TopModelsChart({
	modelData,
	loadingModels,
	rankingsData,
	startTime,
	endTime,
}: {
	modelData: ModelHistogramResponse | null;
	loadingModels: boolean;
	rankingsData: ModelRankingsResponse | null;
	startTime: number;
	endTime: number;
}) {
	const { chartData, displayModels } = useMemo(() => {
		if (!modelData?.buckets || !modelData.bucket_size_seconds) {
			return { chartData: [], displayModels: [] };
		}

		const models = [...(modelData.models || [])].sort((a, b) => a.localeCompare(b));
		const processed = modelData.buckets.map((bucket, index) => {
			const item: any = {
				...bucket,
				index,
				formattedTime: formatTimestamp(bucket.timestamp, modelData.bucket_size_seconds),
			};
			for (const [modelIdx, model] of models.entries()) {
				item[`model_${modelIdx}`] = bucket.by_model?.[model]?.total || 0;
			}
			return item;
		});

		return { chartData: processed, displayModels: models };
	}, [modelData]);

	// Compute totals per model for the ranked legend (aggregate across providers)
	const modelTotals = useMemo(() => {
		if (!rankingsData?.rankings) return [];
		const byModel = new Map<string, number>();
		for (const r of rankingsData.rankings) {
			byModel.set(r.model, (byModel.get(r.model) || 0) + r.total_requests);
		}
		const totalRequests = [...byModel.values()].reduce((sum, v) => sum + v, 0);
		return [...byModel.entries()]
			.sort((a, b) => b[1] - a[1])
			.slice(0, 10)
			.map(([model, total], idx) => ({
				model,
				total,
				pct: totalRequests > 0 ? (total / totalRequests) * 100 : 0,
				colorIdx: displayModels.indexOf(model) >= 0 ? displayModels.indexOf(model) : idx,
			}));
	}, [rankingsData, displayModels]);

	return (
		<ChartCard title="Top Models" loading={loadingModels} testId="dashboard-rankings-top-models" height="100%" className="z-[1]">
			<div style={{ height: 200, marginBottom: 6 }}>
				{chartData.length > 0 ? (
					<ChartErrorBoundary resetKey={`${startTime}-${endTime}-${chartData.length}`}>
						<ResponsiveContainer width="100%" height="100%">
							<BarChart data={chartData} margin={{ top: 6, right: 4, left: 4, bottom: 0 }} barCategoryGap={1}>
								<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
								<XAxis
									dataKey="index"
									type="number"
									domain={[-0.5, chartData.length - 0.5]}
									tick={{ fontSize: 11, className: "fill-zinc-500", dy: 5 }}
									tickLine={false}
									axisLine={false}
									tickFormatter={(idx) => chartData[Math.round(idx)]?.formattedTime || ""}
									interval="preserveStartEnd"
								/>
								<YAxis
									tick={{ fontSize: 11, className: "fill-zinc-500" }}
									tickLine={false}
									axisLine={false}
									width={48}
									tickFormatter={(v) => formatNumber(v)}
									domain={[0, (dataMax: number) => Math.max(dataMax, 1)]}
									allowDataOverflow={false}
								/>
								<Tooltip content={<UsageShareTooltip models={displayModels} />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								{displayModels.map((model, idx) => (
									<Bar
										key={model}
										dataKey={`model_${idx}`}
										stackId="models"
										fill={getModelColor(idx)}
										fillOpacity={0.9}
										isAnimationActive={false}
										barSize={30}
										radius={idx === displayModels.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]}
									/>
								))}
							</BarChart>
						</ResponsiveContainer>
					</ChartErrorBoundary>
				) : (
					<div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>
				)}
			</div>
			<div className="py-2">
				{/* Ranked model legend */}
				{modelTotals.length > 0 && (
					<div className="mt-3 grid grid-cols-2 gap-x-8 gap-y-1.5 px-2 pb-1">
						{modelTotals.map((m, idx) => (
							<div key={m.model} className="flex items-center gap-2 text-sm">
								<span className="text-muted-foreground w-4 text-right text-xs">{idx + 1}.</span>
								<span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(m.colorIdx) }} />
								<span className="min-w-0 flex-1 truncate font-medium">{m.model}</span>
								<span className="shrink-0 text-right text-xs tabular-nums">
									<span className="font-medium">{formatNumber(m.total)}</span>
									<span className="text-muted-foreground ml-1">{m.pct.toFixed(1)}%</span>
								</span>
							</div>
						))}
					</div>
				)}
			</div>
		</ChartCard>
	);
}

export function ModelRankingsTab({ rankingsData, loading, modelData, loadingModels, startTime, endTime }: ModelRankingsTabProps) {
	const [sortField, setSortField] = useState<SortField>("total_requests");
	const [sortOrder, setSortOrder] = useState<SortOrder>("desc");

	const handleSort = useCallback(
		(field: SortField) => {
			if (sortField === field) {
				setSortOrder((prev) => (prev === "desc" ? "asc" : "desc"));
			} else {
				setSortField(field);
				setSortOrder("desc");
			}
		},
		[sortField],
	);

	const sortedRankings = useMemo(() => {
		if (!rankingsData?.rankings) return [];
		return [...rankingsData.rankings].sort((a, b) => {
			const aVal = a[sortField];
			const bVal = b[sortField];
			return sortOrder === "desc" ? (bVal as number) - (aVal as number) : (aVal as number) - (bVal as number);
		});
	}, [rankingsData, sortField, sortOrder]);

	return (
		<div className="flex flex-col gap-4">
			{/* Top Models chart */}
			<TopModelsChart
				modelData={modelData}
				loadingModels={loadingModels}
				rankingsData={rankingsData}
				startTime={startTime}
				endTime={endTime}
			/>

			{/* Rankings table */}
			{loading ? (
				<Card className="rounded-sm p-4 shadow-none">
					<div className="space-y-3">
						<Skeleton className="h-6 w-48" />
						<Skeleton className="h-[300px] w-full" />
					</div>
				</Card>
			) : !rankingsData?.rankings?.length ? (
				<Card className="rounded-sm p-4 shadow-none">
					<div className="text-muted-foreground flex h-[200px] items-center justify-center text-sm">
						No model usage data available for this time period.
					</div>
				</Card>
			) : (
				<Card className="rounded-sm p-2 shadow-none" data-testid="dashboard-model-rankings-table">
					<span className="text-primary pl-2 text-sm font-medium">Model Rankings</span>
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead className="w-12">#</TableHead>
								<TableHead>Model</TableHead>
								<TableHead className="text-right">
									<SortableHeader
										label="Requests"
										field="total_requests"
										currentSort={sortField}
										currentOrder={sortOrder}
										onSort={handleSort}
									/>
								</TableHead>
								<TableHead className="text-right">
									<SortableHeader
										label="Success Rate"
										field="success_rate"
										currentSort={sortField}
										currentOrder={sortOrder}
										onSort={handleSort}
									/>
								</TableHead>
								<TableHead className="text-right">
									<SortableHeader
										label="Tokens"
										field="total_tokens"
										currentSort={sortField}
										currentOrder={sortOrder}
										onSort={handleSort}
									/>
								</TableHead>
								<TableHead className="text-right">
									<SortableHeader label="Cost" field="total_cost" currentSort={sortField} currentOrder={sortOrder} onSort={handleSort} />
								</TableHead>
								<TableHead className="text-right">
									<SortableHeader
										label="Avg Latency"
										field="avg_latency"
										currentSort={sortField}
										currentOrder={sortOrder}
										onSort={handleSort}
									/>
								</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{sortedRankings.map((entry: ModelRankingEntry, index: number) => (
								<TableRow key={`${entry.provider}:${entry.model}`}>
									<TableCell className="text-muted-foreground font-mono text-xs">{index + 1}</TableCell>
									<TableCell>
										<div className="flex items-center gap-2">
											{entry.provider in ProviderIcons ? (
												<RenderProviderIcon provider={entry.provider as ProviderIconType} size="xs" className="h-4 w-4 shrink-0" />
											) : (
												<span className="text-muted-foreground shrink-0 text-xs">{entry.provider}</span>
											)}
											<span className="font-medium">{entry.model}</span>
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											<span>{formatNumber(entry.total_requests)}</span>
											<TrendBadge value={entry.trend.requests_trend} isNew={!entry.trend.has_previous_period} />
										</div>
									</TableCell>
									<TableCell className="text-right">
										<span
											className={
												entry.success_rate >= 99
													? "text-emerald-600 dark:text-emerald-400"
													: entry.success_rate >= 95
														? "text-yellow-600 dark:text-yellow-400"
														: "text-red-600 dark:text-red-400"
											}
										>
											{entry.success_rate.toFixed(1)}%
										</span>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											<span>{formatNumber(entry.total_tokens)}</span>
											<TrendBadge value={entry.trend.tokens_trend} isNew={!entry.trend.has_previous_period} />
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											<span>{formatCost(entry.total_cost)}</span>
											<TrendBadge value={entry.trend.cost_trend} positiveIsGood={false} isNew={!entry.trend.has_previous_period} />
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											<span>{formatLatency(entry.avg_latency)}</span>
											<TrendBadge value={entry.trend.latency_trend} positiveIsGood={false} isNew={!entry.trend.has_previous_period} />
										</div>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				</Card>
			)}
		</div>
	);
}