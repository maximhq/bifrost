"use client";

import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { TokenUsageData } from "@/lib/data/analytics-data";

interface TokenUsageChartProps {
  data: TokenUsageData[];
}

export function TokenUsageChart({ data }: TokenUsageChartProps) {
  return (
    <div className="w-full h-80">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={data}
          margin={{
            top: 10,
            right: 30,
            left: 0,
            bottom: 0,
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
            formatter={(value: number, name: string) => [
              value.toLocaleString(),
              name,
            ]}
          />
          <Legend className="text-xs" />
          <Area
            type="monotone"
            dataKey="promptTokens"
            stackId="1"
            stroke="#6B7280"
            fill="#6B7280"
            fillOpacity={0.6}
            name="Prompt Tokens"
          />
          <Area
            type="monotone"
            dataKey="completionTokens"
            stackId="1"
            stroke="#8B5CF6"
            fill="#8B5CF6"
            fillOpacity={0.6}
            name="Completion Tokens"
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
