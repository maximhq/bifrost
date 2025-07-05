"use client";

import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { RequestVolumeData } from "@/lib/data/analytics-data";

interface RequestVolumeChartProps {
  data: RequestVolumeData[];
}

export function RequestVolumeChart({ data }: RequestVolumeChartProps) {
  return (
    <div className="w-full h-80">
      <ResponsiveContainer width="100%" height="100%">
        <LineChart
          data={data}
          margin={{
            top: 5,
            right: 30,
            left: 20,
            bottom: 5,
          }}
        >
          <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
          <XAxis
            dataKey="date"
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
          />
          <Legend className="text-xs" />
          <Line
            type="monotone"
            dataKey="requests"
            stroke="#6B7280"
            strokeWidth={2}
            dot={{ fill: "#6B7280", strokeWidth: 2, r: 4 }}
            name="Total Requests"
          />
          <Line
            type="monotone"
            dataKey="successful"
            stroke="#10B981"
            strokeWidth={2}
            dot={{ fill: "#10B981", strokeWidth: 2, r: 4 }}
            name="Successful"
          />
          <Line
            type="monotone"
            dataKey="failed"
            stroke="#EF4444"
            strokeWidth={2}
            dot={{ fill: "#EF4444", strokeWidth: 2, r: 4 }}
            name="Failed"
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
