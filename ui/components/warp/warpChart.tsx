import { getModelColor } from "@/app/workspace/dashboard/utils/chartUtils";
import type { ReactNode } from "react";
import { Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { formatWarpChartValue, formatWarpChartX, type WarpChartSpec } from "./warpStream.utils";

/**
 * A chart from Warp's render_chart tool.
 *
 * The spec is data a tool read - the server swaps it in for the id the model
 * pasted - so this only draws: line for a metric over time, bar for a metric
 * across a group, largest first. `renderLink` is the message's own link
 * renderer, so "Open in Logs" follows the router like every other Warp link.
 */
export default function WarpChart({
	spec,
	renderLink,
}: {
	spec: WarpChartSpec;
	renderLink: (href: string, children: ReactNode) => ReactNode;
}) {
	const data = spec.points.map((point) => ({ name: formatWarpChartX(spec, point), value: point.y }));
	const color = getModelColor(0);
	const formatValue = (value: number) => formatWarpChartValue(spec.unit, value);

	return (
		<figure className="my-3 rounded-sm border p-3" data-testid="warp-chart" data-chart-kind={spec.kind}>
			<figcaption className="mb-2 flex items-center justify-between gap-2 text-xs">
				<span className="text-foreground font-medium" data-testid="warp-chart-title">
					{spec.title}
				</span>
				{spec.link ? <span className="shrink-0">{renderLink(spec.link, "Open in Logs")}</span> : null}
			</figcaption>
			{data.length === 0 ? (
				<div className="text-muted-foreground py-6 text-center text-xs" data-testid="warp-chart-empty">
					No data in this window.
				</div>
			) : (
				<div className="h-[200px] w-full">
					<ResponsiveContainer width="100%" height="100%">
						{spec.kind === "line" ? (
							<LineChart data={data} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
								<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-800" />
								<XAxis dataKey="name" tick={{ fontSize: 10 }} tickLine={false} axisLine={false} minTickGap={16} />
								<YAxis tick={{ fontSize: 10 }} tickLine={false} axisLine={false} width={48} tickFormatter={formatValue} />
								<Tooltip formatter={(value) => [formatValue(Number(value)), spec.metric]} labelClassName="text-xs" />
								<Line type="monotone" dataKey="value" stroke={color} strokeWidth={2} dot={data.length <= 31} isAnimationActive={false} />
							</LineChart>
						) : (
							<BarChart data={data} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
								<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-800" />
								{/* Group bars label every bar; bars over time can run to dozens, so they thin out like a line. */}
								<XAxis
									dataKey="name"
									tick={{ fontSize: 10 }}
									tickLine={false}
									axisLine={false}
									{...(spec.interval ? { minTickGap: 16 } : { interval: 0, height: 36 })}
								/>
								<YAxis tick={{ fontSize: 10 }} tickLine={false} axisLine={false} width={48} tickFormatter={formatValue} />
								<Tooltip formatter={(value) => [formatValue(Number(value)), spec.metric]} labelClassName="text-xs" />
								<Bar dataKey="value" fill={color} radius={[2, 2, 0, 0]} isAnimationActive={false} />
							</BarChart>
						)}
					</ResponsiveContainer>
				</div>
			)}
		</figure>
	);
}