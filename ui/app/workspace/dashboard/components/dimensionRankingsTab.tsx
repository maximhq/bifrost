import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { DimensionRankingEntry, DimensionRankingsResponse } from "@/lib/types/logs";
import { COMPACT_NUMBER_FORMAT, formatCompactNumber as formatNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { memo, useCallback, useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { getModelColor } from "../utils/chartUtils";
import { ChartCard } from "./charts/chartCard";
import { ChartErrorBoundary } from "./charts/chartErrorBoundary";
import { formatCost, SortableHeader, TrendBadge } from "./rankingsShared";

type SortField = "total_requests" | "total_tokens" | "total_cost";
type SortOrder = "asc" | "desc";

interface DimensionRankingsTabProps {
	data: DimensionRankingsResponse | null;
	loading: boolean;
	dimensionLabel: string;
	testIdPrefix: string;
	attributed?: boolean;
}

function TopDimensionTooltip({ active, payload }: any) {
	if (!active || !payload || !payload.length) return null;
	const data = payload[0]?.payload;
	if (!data) return null;
	return (
		<div className="rounded-sm border border-zinc-200 bg-white px-3 py-2 shadow-lg dark:border-zinc-700 dark:bg-zinc-900">
			<div className="mb-1 text-xs text-zinc-500">{data.displayName}</div>
			<div className="text-sm font-medium">{data.total_requests.toLocaleString()} requests</div>
		</div>
	);
}

function TopDimensionChart({
	data,
	loading,
	dimensionLabel,
	testIdPrefix,
	attributed,
}: {
	data: DimensionRankingsResponse | null;
	loading: boolean;
	dimensionLabel: string;
	testIdPrefix: string;
	attributed?: boolean;
}) {
	const { chartData, grandTotal, rankedItems, actualTotal, attributedTotal } = useMemo(() => {
		if (!data?.rankings?.length) return { chartData: [], grandTotal: null, rankedItems: [], actualTotal: null, attributedTotal: null };

		const sorted = [...data.rankings].sort((a, b) => b.total_requests - a.total_requests);
		const top = sorted.slice(0, 10);
		const total = sorted.reduce((sum, r) => sum + r.total_requests, 0);

		const items = top.map((entry, idx) => ({
			displayName: entry.name || entry.id,
			id: entry.id,
			total_requests: entry.total_requests,
			pct: total > 0 ? (entry.total_requests / total) * 100 : 0,
			colorIdx: idx,
		}));

		const chart = items.map((item) => ({
			...item,
			fill: getModelColor(item.colorIdx),
		}));

		// Server-computed totals (fan-out dimensions only); when absent, fall
		// back to the client-side attributed sum.
		const actual = attributed ? (data.total_actual_requests ?? null) : null;
		const attributedSum = actual !== null ? (data.total_attributed_requests ?? total) : total;

		return { chartData: chart, grandTotal: total, rankedItems: items, actualTotal: actual, attributedTotal: attributedSum };
	}, [data, attributed]);

	return (
		<ChartCard
			title={`Top ${dimensionLabel}s`}
			loading={loading}
			testId={`${testIdPrefix}-top-chart`}
			className="z-[1] h-full"
			totalLabel={attributed && actualTotal === null ? "Total Requests (attributed)" : "Total Requests"}
			total={
				actualTotal !== null ? (
					<NumberFlow value={actualTotal} format={COMPACT_NUMBER_FORMAT} />
				) : grandTotal !== null ? (
					<NumberFlow value={grandTotal} format={COMPACT_NUMBER_FORMAT} />
				) : undefined
			}
			totalTooltip={
				grandTotal === null ? undefined : actualTotal !== null ? (
					<div className="max-w-[240px] text-xs opacity-80">Actual number of requests sent</div>
				) : attributed ? (
					<div className="space-y-1">
						<div className="max-w-[240px] text-xs opacity-80">
							Attributed - a request counts toward each {dimensionLabel.toLowerCase()} it belongs to, so this can exceed the actual request
							count.
						</div>
					</div>
				) : (
					grandTotal.toLocaleString("en-US")
				)
			}
			secondaryTotalLabel="Attributed Requests"
			secondaryTotal={actualTotal !== null ? <NumberFlow value={attributedTotal ?? 0} format={COMPACT_NUMBER_FORMAT} /> : undefined}
			secondaryTotalTooltip={
				actualTotal === null ? undefined : (
					<div className="space-y-1">
						<div className="max-w-[240px] text-xs opacity-80">
							A request counts toward each {dimensionLabel.toLowerCase()} it belongs to, so this can exceed the total request count.
						</div>
					</div>
				)
			}
		>
			<div style={{ height: Math.max(200, chartData.length * 40 + 40), marginBottom: 6 }}>
				{chartData.length > 0 ? (
					<ChartErrorBoundary resetKey={`${chartData.length}`}>
						<ResponsiveContainer width="100%" height="100%">
							<BarChart data={chartData} layout="vertical" margin={{ top: 6, right: 20, left: 0, bottom: 0 }} barCategoryGap={4}>
								<CartesianGrid strokeDasharray="3 3" horizontal={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
								<XAxis
									type="number"
									tick={{ fontSize: 11, className: "fill-zinc-500" }}
									tickLine={false}
									axisLine={false}
									tickFormatter={(v) => formatNumber(v)}
								/>
								<YAxis
									type="category"
									dataKey="displayName"
									tick={(props: any) => {
										const { x, y, payload } = props;
										const labelWidth = 92;
										return (
											<foreignObject x={x - labelWidth} y={y - 9} width={labelWidth} height={18} style={{ overflow: "visible" }}>
												<div
													title={payload.value}
													className="truncate text-right text-[11px] leading-[18px] text-zinc-500 dark:text-zinc-400"
													style={{ width: labelWidth }}
												>
													{payload.value}
												</div>
											</foreignObject>
										);
									}}
									tickLine={false}
									axisLine={false}
									width={100}
								/>
								<Tooltip content={<TopDimensionTooltip />} cursor={{ fill: "#8c8c8f", fillOpacity: 0.15 }} />
								<Bar dataKey="total_requests" isAnimationActive={false} barSize={24} radius={[0, 4, 4, 0]}>
									{chartData.map((entry, idx) => (
										<Cell key={entry.id} fill={getModelColor(idx)} />
									))}
								</Bar>
							</BarChart>
						</ResponsiveContainer>
					</ChartErrorBoundary>
				) : (
					<div className="text-muted-foreground flex h-full items-center justify-center text-sm">No data available</div>
				)}
			</div>
			<div className="py-2">
				{rankedItems.length > 0 && (
					<div className="mt-3 grid grid-cols-2 gap-x-8 gap-y-1.5 px-2 pb-1">
						{rankedItems.map((item, idx) => (
							<div key={item.id} className="flex items-center gap-2 text-sm">
								<span className="text-muted-foreground w-4 text-right text-xs">{idx + 1}.</span>
								<span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: getModelColor(item.colorIdx) }} />
								<span className="min-w-0 flex-1 truncate font-medium">{item.displayName}</span>
								<span className="shrink-0 text-right text-xs tabular-nums">
									<span className="font-medium">{formatNumber(item.total_requests)}</span>
									<span className="text-muted-foreground ml-1">{item.pct.toFixed(1)}%</span>
								</span>
							</div>
						))}
					</div>
				)}
			</div>
		</ChartCard>
	);
}

function DimensionRankingsTabImpl({ data, loading, dimensionLabel, testIdPrefix, attributed }: DimensionRankingsTabProps) {
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
		if (!data?.rankings) return [];
		return [...data.rankings].sort((a, b) => {
			const aVal = a[sortField];
			const bVal = b[sortField];
			return sortOrder === "desc" ? (bVal as number) - (aVal as number) : (aVal as number) - (bVal as number);
		});
	}, [data, sortField, sortOrder]);

	return (
		<div className="flex flex-col gap-4">
			<TopDimensionChart
				data={data}
				loading={loading}
				dimensionLabel={dimensionLabel}
				testIdPrefix={testIdPrefix}
				attributed={attributed}
			/>

			{loading ? (
				<Card className="rounded-sm p-4 shadow-none">
					<div className="space-y-3">
						<Skeleton className="h-6 w-48" />
						<Skeleton className="h-[300px] w-full" />
					</div>
				</Card>
			) : !data?.rankings?.length ? (
				<Card className="rounded-sm p-4 shadow-none">
					<div className="text-muted-foreground flex h-[200px] items-center justify-center text-sm">
						No {dimensionLabel.toLowerCase()} usage data available for this time period.
					</div>
				</Card>
			) : (
				<Card className="rounded-sm p-2 shadow-none" data-testid={`${testIdPrefix}-table`}>
					<span className="text-primary pl-2 text-sm font-medium">{dimensionLabel} Rankings</span>
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead className="w-12">#</TableHead>
								<TableHead>{dimensionLabel}</TableHead>
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
							</TableRow>
						</TableHeader>
						<TableBody>
							{sortedRankings.map((entry: DimensionRankingEntry, index: number) => (
								<TableRow key={entry.id}>
									<TableCell className="text-muted-foreground font-mono text-xs">{index + 1}</TableCell>
									<TableCell>
										<div className="flex flex-col">
											<span className="font-medium">{entry.name || entry.id}</span>
											{entry.name && entry.name !== entry.id && <span className="text-muted-foreground text-xs">{entry.id}</span>}
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											<span>{formatNumber(entry.total_requests)}</span>
											<TrendBadge value={entry.trend.requests_trend} isNew={!entry.trend.has_previous_period} />
										</div>
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
								</TableRow>
							))}
						</TableBody>
					</Table>
				</Card>
			)}
		</div>
	);
}

export const DimensionRankingsTab = memo(DimensionRankingsTabImpl);