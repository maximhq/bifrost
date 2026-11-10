import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { useTranslation } from "react-i18next";
import { RUN_SERIES_COLORS, type RunTrendPoint, type RunTrendSeries } from "./runUtils";

// The key of the "rows passed" line among a point's values.
const ROWS = "rows";

export const trendColor = (index: number) => RUN_SERIES_COLORS[index % RUN_SERIES_COLORS.length];

interface Line {
	key: string;
	name: string;
	color: string;
}

function TrendTooltip({ active, payload, lines }: { active?: boolean; payload?: { payload?: RunTrendPoint }[]; lines: Line[] }) {
	const point = active ? payload?.[0]?.payload : undefined;
	if (!point) return null;
	return (
		<div className="bg-popover rounded-sm border px-3 py-2 text-xs shadow-lg">
			<div className="mb-1 font-medium">{point.name}</div>
			{lines.map((line) => {
				const value = point[line.key];
				if (typeof value !== "number") return null;
				return (
					<div key={line.key} className="flex items-center justify-between gap-4">
						<span className="text-muted-foreground flex items-center gap-1.5">
							<span className="h-2 w-2 rounded-full" style={{ background: line.color }} />
							{line.name}
						</span>
						<span className="font-mono">{Math.round(value)}%</span>
					</div>
				);
			})}
		</div>
	);
}

// RunsTrend draws how pass rates moved across the runs on the page, oldest on the
// left: one line for rows passed, and one for each of the most used evaluators.
export function RunsTrend({ points, series }: { points: RunTrendPoint[]; series: RunTrendSeries[] }) {
	const { t } = useTranslation("config");
	const lines: Line[] = [
		{ key: ROWS, name: t("evals.runs.list.columns.passed"), color: trendColor(0) },
		...series.map((item, index) => ({ key: item.id, name: item.name, color: trendColor(index + 1) })),
	];
	const rowRates = points.map((point) => point.rows).filter((value): value is number => value !== null);
	const averages = new Map<string, number>(series.map((item) => [item.id, item.average]));
	if (rowRates.length > 0) averages.set(ROWS, rowRates.reduce((sum, value) => sum + value, 0) / rowRates.length);
	return (
		<div className="flex shrink-0 flex-wrap items-stretch gap-4 rounded-sm border px-4 py-3" data-testid="runs-trend">
			<div className="flex min-w-64 flex-1 flex-col gap-1">
				<h2 className="text-sm font-medium">{t("evals.runs.list.trendTitle")}</h2>
				<div className="h-32">
					<ResponsiveContainer width="100%" height="100%" initialDimension={{ width: 1, height: 1 }}>
						<LineChart data={points} margin={{ top: 6, right: 8, left: 0, bottom: 0 }}>
							<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
							<XAxis
								dataKey="name"
								tick={{ fontSize: 11, className: "fill-zinc-500" }}
								tickLine={false}
								axisLine={false}
								interval="preserveStartEnd"
								tickFormatter={(name: string) => (name.length > 14 ? `${name.slice(0, 13)}…` : name)}
							/>
							<YAxis
								domain={[0, 100]}
								ticks={[0, 50, 100]}
								tick={{ fontSize: 11, className: "fill-zinc-500" }}
								tickLine={false}
								axisLine={false}
								width={40}
								tickFormatter={(value: number) => `${value}%`}
							/>
							<Tooltip content={<TrendTooltip lines={lines} />} cursor={{ stroke: "#8c8c8f", strokeOpacity: 0.4 }} />
							{lines.map((line) => (
								<Line
									key={line.key}
									dataKey={line.key}
									type="monotone"
									stroke={line.color}
									strokeWidth={line.key === ROWS ? 2 : 1.5}
									dot={{ r: 2.5, fill: line.color, strokeWidth: 0 }}
									activeDot={{ r: 4 }}
									connectNulls
									isAnimationActive={false}
								/>
							))}
						</LineChart>
					</ResponsiveContainer>
				</div>
			</div>
			<ul className="flex w-52 shrink-0 flex-col justify-center gap-1.5 text-sm">
				{lines.map((line) => {
					const average = averages.get(line.key);
					return (
						<li key={line.key} className="flex items-center justify-between gap-3" data-testid={`runs-trend-series-${line.name}`}>
							<span className="flex min-w-0 items-center gap-2">
								<span className="h-2 w-2 shrink-0 rounded-full" style={{ background: line.color }} />
								<span className="truncate">{line.name}</span>
							</span>
							<span className="text-muted-foreground font-mono text-xs whitespace-nowrap">
								{average === undefined ? "–" : `${Math.round(average)}%`} {t("evals.runs.compare.avg")}
							</span>
						</li>
					);
				})}
			</ul>
		</div>
	);
}