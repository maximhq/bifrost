"use client";

import { Badge } from "@/components/ui/badge";
import { CardHeader, CardTitle } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels } from "@/lib/constants/governance";
import { useGetProviderGovernanceQuery, useLazyGetCoreConfigQuery } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useState } from "react";

interface Props {
	className?: string;
	provider: ModelProvider;
}

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

// Circular progress component
function CircularProgress({
	value,
	max,
	size = 80,
	strokeWidth = 6,
	isExhausted = false,
}: {
	value: number;
	max: number;
	size?: number;
	strokeWidth?: number;
	isExhausted?: boolean;
}) {
	const percentage = max > 0 ? Math.min((value / max) * 100, 100) : 0;
	const radius = (size - strokeWidth) / 2;
	const circumference = radius * 2 * Math.PI;
	const strokeDashoffset = circumference - (percentage / 100) * circumference;

	return (
		<div className="relative" style={{ width: size, height: size }}>
			<svg width={size} height={size} className="-rotate-90 transform">
				{/* Background circle */}
				<circle
					cx={size / 2}
					cy={size / 2}
					r={radius}
					fill="none"
					stroke="currentColor"
					strokeWidth={strokeWidth}
					className="text-muted/70 dark:text-muted/30"
				/>
				{/* Progress circle */}
				<circle
					cx={size / 2}
					cy={size / 2}
					r={radius}
					fill="none"
					stroke="currentColor"
					strokeWidth={strokeWidth}
					strokeDasharray={circumference}
					strokeDashoffset={strokeDashoffset}
					strokeLinecap="round"
					className={cn(
						"transition-all duration-500",
						isExhausted ? "text-red-500/70" : percentage > 80 ? "text-amber-500/70" : "text-emerald-500/70",
					)}
				/>
			</svg>
			<div className="absolute inset-0 flex items-center justify-center">
				<span
					className={cn("text-lg font-medium", isExhausted ? "text-red-500/70" : percentage > 80 ? "text-amber-500/70" : "text-foreground")}
				>
					{Math.round(percentage)}%
				</span>
			</div>
		</div>
	);
}

// Metric card component
function MetricCard({
	title,
	value,
	max,
	unit,
	resetDuration,
	isExhausted,
}: {
	title: string;
	value: number;
	max: number;
	unit: string;
	resetDuration: string;
	isExhausted: boolean;
}) {
	// Compute safe percentage to avoid division by zero
	const percentage = max > 0 ? Math.round((value / max) * 100) : 0;
	const clampedPercentage = Math.max(0, Math.min(100, percentage));

	return (
		<div
			className={cn(
				"group relative overflow-hidden rounded-sm border p-5 transition-all duration-300",
				"hover:shadow-lg hover:shadow-black/5",
				isExhausted ? "border-red-500/30 bg-red-500/5" : "border-border/50 bg-card hover:border-border",
			)}
		>
			{/* Subtle gradient overlay */}
			<div className="from-primary/5 pointer-events-none absolute inset-0 bg-gradient-to-br to-transparent opacity-0 transition-opacity group-hover:opacity-100" />

			<div className="relative flex items-start justify-between gap-4">
				<div className="flex-1 space-y-3">
					<div className="flex flex-wrap items-center gap-2">
						<span className="text-muted-foreground text-sm font-medium whitespace-nowrap">{title}</span>
						{isExhausted && (
							<Badge variant="destructive" className="text-xs whitespace-nowrap">
								Exhausted
							</Badge>
						)}
					</div>

					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<div className="space-y-1">
									<div className="flex items-baseline gap-1">
										<span className="text-2xl font-medium tracking-tight">
											{unit === "$" ? formatCurrency(value) : value.toLocaleString()}
										</span>
										<span className="text-muted-foreground text-sm">
											/ {unit === "$" ? formatCurrency(max) : `${max.toLocaleString()} ${unit}`}
										</span>
									</div>
									<div className="text-xs">
										<span className="text-muted-foreground">Resets {formatResetDuration(resetDuration)}</span>
									</div>
								</div>
							</TooltipTrigger>
							<TooltipContent side="bottom">
								<p className="font-medium">
									{clampedPercentage}% of {title.toLowerCase()} used
								</p>
							</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				</div>

				<CircularProgress value={value} max={max} isExhausted={isExhausted} />
			</div>
		</div>
	);
}

export default function ProviderGovernanceTable({ provider, className }: Props) {
	const hasViewAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const [governanceEnabled, setGovernanceEnabled] = useState<boolean | null>(null);
	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

	// Check if governance is enabled
	useEffect(() => {
		triggerGetConfig({ fromDB: true })
			.then((res) => {
				setGovernanceEnabled(!!res.data?.client_config?.enable_governance);
			})
			.catch(() => {
				setGovernanceEnabled(false);
			});
	}, [triggerGetConfig]);

	const { data: providerGovernanceData, isLoading } = useGetProviderGovernanceQuery(undefined, {
		skip: !hasViewAccess || !governanceEnabled,
		pollingInterval: hasViewAccess && governanceEnabled ? 10000 : 0,
		refetchOnFocus: true,
		skipPollingIfUnfocused: true,
	});

	// Find governance data for this provider
	const providerGovernance = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);

	// Check if any governance is configured
	const hasGovernance = providerGovernance?.budget || providerGovernance?.rate_limit;

	if (isLoading || governanceEnabled === null) {
		return (
			<div className={cn("w-full", className)}>
				<CardHeader className="mb-4 px-0">
					<CardTitle className="flex items-center justify-between">
						<div className="flex items-center gap-2">Governance</div>
					</CardTitle>
				</CardHeader>
				<div className="flex items-center justify-center py-12">
					<div className="border-primary h-6 w-6 animate-spin rounded-full border-2 border-t-transparent" />
				</div>
			</div>
		);
	}

	// Governance not enabled or no governance configured - don't show the section
	if (!governanceEnabled || !hasGovernance) {
		return null;
	}

	const budget = providerGovernance?.budget;
	const rateLimit = providerGovernance?.rate_limit;

	const isBudgetExhausted = !!(budget?.max_limit && budget.max_limit > 0 && budget.current_usage >= budget.max_limit);
	const isTokenExhausted = !!(
		rateLimit?.token_max_limit &&
		rateLimit.token_max_limit > 0 &&
		rateLimit.token_current_usage >= rateLimit.token_max_limit
	);
	const isRequestExhausted = !!(
		rateLimit?.request_max_limit &&
		rateLimit.request_max_limit > 0 &&
		rateLimit.request_current_usage >= rateLimit.request_max_limit
	);

	return (
		<div className={cn("w-full", className)}>
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Governance</div>
				</CardTitle>
			</CardHeader>

			<div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
				{/* Budget Card */}
				{budget && (
					<MetricCard
						title="Budget"
						value={budget.current_usage}
						max={budget.max_limit}
						unit="$"
						resetDuration={budget.reset_duration}
						isExhausted={isBudgetExhausted}
					/>
				)}

				{/* Token Rate Limit Card */}
				{rateLimit?.token_max_limit && (
					<MetricCard
						title="Token Limit"
						value={rateLimit.token_current_usage}
						max={rateLimit.token_max_limit}
						unit="tokens"
						resetDuration={rateLimit.token_reset_duration || "1h"}
						isExhausted={isTokenExhausted}
					/>
				)}

				{/* Request Rate Limit Card */}
				{rateLimit?.request_max_limit && (
					<MetricCard
						title="Request Limit"
						value={rateLimit.request_current_usage}
						max={rateLimit.request_max_limit}
						unit="requests"
						resetDuration={rateLimit.request_reset_duration || "1h"}
						isExhausted={isRequestExhausted}
					/>
				)}
			</div>
		</div>
	);
}
