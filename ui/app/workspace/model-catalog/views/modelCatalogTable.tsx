"use client";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels } from "@/lib/constants/logs";
import { Info } from "lucide-react";

function formatCost(dollars: number) {
	return `$${dollars.toFixed(4)}`;
}

export interface ModelCatalogRow {
	providerName: string;
	isCustom: boolean;
	baseProviderType?: string;
	modelsUsed: string[];
	totalTraffic24h: number;
	totalCost24h: number;
}

interface ModelCatalogTableProps {
	rows: ModelCatalogRow[];
	providers: string[];
	providerFilter: string;
	onProviderFilterChange: (value: string) => void;
	totalProviders: number;
	totalModels: number;
	totalRequests24h: number;
	totalCost24h: number;
	isLoadingModels: boolean;
}

export default function ModelCatalogTable({
	rows,
	providers,
	providerFilter,
	onProviderFilterChange,
	totalProviders,
	totalModels,
	totalRequests24h,
	totalCost24h,
	isLoadingModels,
}: ModelCatalogTableProps) {
	const summaryCards = [
		{ label: "Total Providers", value: totalProviders.toLocaleString() },
		{ label: "Total Models", value: totalModels.toLocaleString() },
		{ label: "Total Requests (24h)", value: totalRequests24h.toLocaleString() },
		{ label: "Total Cost (24h)", value: formatCost(totalCost24h) },
	];

	return (
		<div className="space-y-6">
			{/* Summary Cards */}
			<div className="grid grid-cols-4 gap-4">
				{summaryCards.map((card) => (
					<Card key={card.label} className="py-4 shadow-none">
						<CardContent className="px-4">
							<p className="text-muted-foreground text-xs">{card.label}</p>
							<p className="mt-1 text-xl font-semibold">{card.value}</p>
						</CardContent>
					</Card>
				))}
			</div>

			{/* Header + Filter */}
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-lg font-semibold">Model Catalog</h2>
					<p className="text-muted-foreground text-sm">Overview of all configured providers, models, and usage.</p>
				</div>
				<Select
					value={providerFilter || "all"}
					onValueChange={(val) => onProviderFilterChange(val === "all" ? "" : val)}
					data-testid="model-catalog-provider-filter"
				>
					<SelectTrigger className="w-[200px]" data-testid="model-catalog-provider-trigger">
						<SelectValue placeholder="All Providers" />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="all">All Providers</SelectItem>
						{providers.map((p) => (
							<SelectItem key={p} value={p}>
								{ProviderLabels[p as keyof typeof ProviderLabels] || p}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>

			{/* Table */}
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Provider</TableHead>
							<TableHead>
								<TooltipProvider>
									<div className="flex items-center gap-1">
										Models
										<Tooltip>
											<TooltipTrigger data-testid="model-catalog-models-info-trigger">
												<Info className="text-muted-foreground h-3.5 w-3.5" />
											</TooltipTrigger>
											<TooltipContent side="bottom">Models used in the last 30 days</TooltipContent>
										</Tooltip>
									</div>
								</TooltipProvider>
							</TableHead>
							<TableHead className="text-right">Total Traffic (24h)</TableHead>
							<TableHead className="text-right">Total Cost (24h)</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{rows.length === 0 ? (
							<TableRow>
								<TableCell colSpan={4} className="h-24 text-center">
									<span className="text-muted-foreground text-sm">No matching providers found.</span>
								</TableCell>
							</TableRow>
						) : (
							rows.map((row) => (
								<TableRow key={row.providerName}>
									<TableCell>
										<div className="flex items-center gap-2">
											<RenderProviderIcon
												provider={(row.isCustom ? row.baseProviderType : row.providerName) as ProviderIconType}
												size="sm"
												className="h-4 w-4 shrink-0"
											/>
											<span className="font-medium">
												{row.isCustom
													? row.providerName
													: ProviderLabels[row.providerName as keyof typeof ProviderLabels] || row.providerName}
											</span>
											{row.isCustom && (
												<Badge variant="secondary" className="text-muted-foreground px-1.5 py-0.5 text-[10px] font-bold">
													CUSTOM
												</Badge>
											)}
										</div>
									</TableCell>
									<TableCell>
										{isLoadingModels ? (
											<div className="flex items-center gap-1">
												<Skeleton className="h-5 w-24 rounded-full" />
												<Skeleton className="h-5 w-32 rounded-full" />
												<Skeleton className="h-5 w-20 rounded-full" />
											</div>
										) : (
											<ModelsUsedCell models={row.modelsUsed} />
										)}
									</TableCell>
									<TableCell className="text-right font-mono text-sm">{row.totalTraffic24h.toLocaleString()}</TableCell>
									<TableCell className="text-right font-mono text-sm">{formatCost(row.totalCost24h)}</TableCell>
								</TableRow>
							))
						)}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}

function ModelsUsedCell({ models: rawModels }: { models: string[] }) {
	const models = Array.from(new Set(rawModels.filter(Boolean)));
	if (models.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}

	const MAX_VISIBLE = 3;
	const visible = models.slice(0, MAX_VISIBLE);
	const remaining = models.length - MAX_VISIBLE;

	return (
		<TooltipProvider>
			<div className="flex flex-wrap items-center gap-1">
				{visible.map((m) => (
					<Badge key={m} variant="outline" className="text-xs font-normal">
						{m}
					</Badge>
				))}
				{remaining > 0 && (
					<Tooltip>
						<TooltipTrigger data-testid="model-catalog-models-overflow-trigger">
							<Badge variant="outline" className="text-xs font-normal">
								+{remaining} more
							</Badge>
						</TooltipTrigger>
						<TooltipContent side="bottom" className="max-w-xs">
							{models.slice(MAX_VISIBLE).join(", ")}
						</TooltipContent>
					</Tooltip>
				)}
			</div>
		</TooltipProvider>
	);
}
