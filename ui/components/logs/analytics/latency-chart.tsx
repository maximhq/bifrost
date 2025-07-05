"use client";

import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { LatencyData } from "@/lib/data/analytics-data";

interface LatencyChartProps {
  data: LatencyData[];
}

export function LatencyChart({ data }: LatencyChartProps) {
  return (
    <div className="w-full h-80">
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
          <XAxis
            dataKey="provider"
            className="text-xs fill-muted-foreground"
            axisLine={false}
            tickLine={false}
          />
          <YAxis
            className="text-xs fill-muted-foreground"
            axisLine={false}
            tickLine={false}
          />
          <Tooltip
            contentStyle={{
              backgroundColor: "hsl(var(--card))",
              border: "1px solid hsl(var(--border))",
              borderRadius: "6px",
              fontSize: "12px",
            }}
            formatter={(value: number, name: string) => [
              `${value}ms`,
              name.charAt(0).toUpperCase() + name.slice(1),
            ]}
          />
          <Legend className="text-xs" />
          <Bar
            dataKey="avgLatency"
            fill="#6B7280"
            radius={[4, 4, 0, 0]}
            name="Average Latency"
          />
          <Bar
            dataKey="minLatency"
            fill="#10B981"
            radius={[4, 4, 0, 0]}
            name="Min Latency"
          />
          <Bar
            dataKey="maxLatency"
            fill="#EF4444"
            radius={[4, 4, 0, 0]}
            name="Max Latency"
          />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
