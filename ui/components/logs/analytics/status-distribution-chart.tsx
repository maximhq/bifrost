"use client";

import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Cell } from "recharts";
import { StatusData } from "@/lib/data/analytics-data";

interface StatusDistributionChartProps {
	data: StatusData[];
}

export function StatusDistributionChart({ data }: StatusDistributionChartProps) {
	return (
		<div className="h-80 w-full">
			<ResponsiveContainer width="100%" height="100%">
				<BarChart
					data={data}
					margin={{
						top: 20,
						right: 30,
						left: 20,
						bottom: 5,
					}}
				>
					<CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
					<XAxis dataKey="status" className="fill-muted-foreground text-xs" axisLine={false} tickLine={false} />
					<YAxis className="fill-muted-foreground text-xs" axisLine={false} tickLine={false} />
					<Tooltip
						contentStyle={{
							backgroundColor: "hsl(var(--card))",
							border: "1px solid hsl(var(--border))",
							borderRadius: "6px",
							fontSize: "12px",
						}}
					/>
					<Bar dataKey="count" radius={[4, 4, 0, 0]}>
						{data.map((entry, index) => (
							<Cell key={`cell-${index}`} fill={entry.color} />
						))}
					</Bar>
				</BarChart>
			</ResponsiveContainer>
		</div>
	);
}
