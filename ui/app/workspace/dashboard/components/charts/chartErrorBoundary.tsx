"use client";

import { Component, type ErrorInfo, type ReactNode } from "react";
import { BarChart, CartesianGrid, ResponsiveContainer, XAxis, YAxis } from "recharts";

// Empty chart placeholder when data fails to render
function EmptyChart() {
	return (
		<ResponsiveContainer width="100%" height="100%">
			<BarChart data={[{ name: "", value: 0 }, { name: " ", value: 0 }]}>
				<CartesianGrid strokeDasharray="3 3" vertical={false} className="stroke-zinc-200 dark:stroke-zinc-700" />
				<XAxis dataKey="name" tick={{ fontSize: 13, className: "fill-zinc-500", dy: 5 }} tickLine={false} axisLine={false} />
				<YAxis tick={{ fontSize: 13, className: "fill-zinc-500" }} tickLine={false} axisLine={false} width={40} domain={[0, 1]} />
			</BarChart>
		</ResponsiveContainer>
	);
}

interface ChartErrorBoundaryProps {
	children: ReactNode;
	resetKey?: string;
}

interface ChartErrorBoundaryState {
	hasError: boolean;
	prevResetKey?: string;
}

export class ChartErrorBoundary extends Component<ChartErrorBoundaryProps, ChartErrorBoundaryState> {
	constructor(props: ChartErrorBoundaryProps) {
		super(props);
		this.state = { hasError: false };
	}

	static getDerivedStateFromError(_: Error) {
		return { hasError: true };
	}

	static getDerivedStateFromProps(props: ChartErrorBoundaryProps, state: ChartErrorBoundaryState) {
		// Reset error state when resetKey changes
		if (props.resetKey !== state.prevResetKey) {
			return { hasError: false, prevResetKey: props.resetKey };
		}
		return null;
	}

	componentDidCatch(error: Error, _errorInfo: ErrorInfo) {
		console.warn("Chart rendering error:", error.message);
	}

	render() {
		if (this.state.hasError) {
			return <EmptyChart />;
		}
		return this.props.children;
	}
}
