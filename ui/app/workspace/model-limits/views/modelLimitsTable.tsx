import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getErrorMessage, useDeleteModelConfigMutation, useGetBudgetExtensionsQuery } from "@/lib/store";
import { ModelConfig } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronLeft, ChevronRight, Edit, Plus, Search, Trash2, TrendingUp } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import ExtendBudgetDialog from "./extendBudgetDialog";
import ModelLimitSheet from "./modelLimitSheet";
import { ModelLimitsEmptyState } from "./modelLimitsEmptyState";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

const toTestIdPart = (value: string) =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "");

function ActiveExtensionBadge({ budgetId }: { budgetId: string }) {
	// Poll the extension list every 2s so a deleted/expired extension drops out within ~2s even
	// if the component itself never re-mounts. Combined with the 1s `now` ticker below, the badge
	// disappears within 1s of the local expiry and within ~2s of a server-side deletion.
	const { data, isLoading } = useGetBudgetExtensionsQuery(
		{ budgetId, status: "approved" },
		{ pollingInterval: 2000, refetchOnMountOrArgChange: true, refetchOnFocus: true },
	);
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const id = setInterval(() => setNow(Date.now()), 1000);
		return () => clearInterval(id);
	}, []);
	if (isLoading) return null;
	// Always derive the active extension from the latest API response AND current wall-clock —
	// never trust cached data without re-checking expiry on every render.
	const active = data?.budget_extensions.find((e) => e.status === "approved" && e.expires_at && new Date(e.expires_at).getTime() > now);
	if (!active) return null;
	const expiresAt = new Date(active.expires_at!);
	const diffMs = expiresAt.getTime() - now;
	if (diffMs <= 0) return null;
	const diffH = Math.floor(diffMs / 3600000);
	const diffM = Math.floor((diffMs % 3600000) / 60000);
	const relativeTime = diffH > 0 ? `${diffH}h ${diffM}m` : `${diffM}m`;
	return (
		<span className="mt-1 flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
			<TrendingUp className="h-3 w-3" />+{formatCurrency(active.amount)}, expires in {relativeTime}
		</span>
	);
}

interface ModelLimitsTableProps {
	modelConfigs: ModelConfig[];
	totalCount: number;
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function ModelLimitsTable({
	modelConfigs,
	totalCount,
	search,
	debouncedSearch,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
}: ModelLimitsTableProps) {
	const [showModelLimitSheet, setShowModelLimitSheet] = useState(false);
	const [editingModelConfigId, setEditingModelConfigId] = useState<string | null>(null);
	const [extendBudgetModelConfigId, setExtendBudgetModelConfigId] = useState<string | null>(null);

	// Derive editingModelConfig from props so it stays in sync with RTK cache updates
	const editingModelConfig = useMemo(
		() => (editingModelConfigId ? (modelConfigs.find((mc) => mc.id === editingModelConfigId) ?? null) : null),
		[editingModelConfigId, modelConfigs],
	);

	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const [deleteModelConfig, { isLoading: isDeleting }] = useDeleteModelConfigMutation();

	const handleDelete = async (id: string) => {
		try {
			await deleteModelConfig(id).unwrap();
			toast.success("Model limit deleted successfully");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddModelLimit = () => {
		setEditingModelConfigId(null);
		setShowModelLimitSheet(true);
	};

	const handleEditModelLimit = (config: ModelConfig, e: React.MouseEvent) => {
		e.stopPropagation();
		setEditingModelConfigId(config.id);
		setShowModelLimitSheet(true);
	};

	const handleModelLimitSaved = () => {
		setShowModelLimitSheet(false);
		setEditingModelConfigId(null);
	};

	const extendBudgetConfig = useMemo(
		() => (extendBudgetModelConfigId ? (modelConfigs.find((mc) => mc.id === extendBudgetModelConfigId) ?? null) : null),
		[extendBudgetModelConfigId, modelConfigs],
	);

	const hasActiveFilters = debouncedSearch;

	// True empty state: no model limits at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				{showModelLimitSheet && (
					<ModelLimitSheet modelConfig={editingModelConfig} onSave={handleModelLimitSaved} onCancel={() => setShowModelLimitSheet(false)} />
				)}
				<ModelLimitsEmptyState onAddClick={handleAddModelLimit} canCreate={hasCreateAccess} />
			</>
		);
	}

	return (
		<>
			{showModelLimitSheet && (
				<ModelLimitSheet modelConfig={editingModelConfig} onSave={handleModelLimitSaved} onCancel={() => setShowModelLimitSheet(false)} />
			)}
			{extendBudgetConfig?.budget && (
				<ExtendBudgetDialog
					open={extendBudgetModelConfigId !== null}
					onOpenChange={(open) => {
						if (!open) setExtendBudgetModelConfigId(null);
					}}
					budgetId={extendBudgetConfig.budget.id}
					modelName={extendBudgetConfig.model_name}
				/>
			)}

			<div className="space-y-4">
				<div className="flex items-center justify-between">
					<div>
						<h1 className="text-lg font-semibold">Model Limits</h1>
						<p className="text-muted-foreground text-sm">
							Configure budgets and rate limits at the model level. For provider-specific limits, visit each provider&apos;s settings.
						</p>
					</div>
					<Button onClick={handleAddModelLimit} disabled={!hasCreateAccess} data-testid="model-limits-button-create">
						<Plus className="h-4 w-4" />
						Add Model Limit
					</Button>
				</div>

				{/* Toolbar: Search */}
				<div className="flex items-center gap-3">
					<div className="relative max-w-sm flex-1">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label="Search model limits by model name"
							placeholder="Search by model name..."
							value={search}
							onChange={(e) => onSearchChange(e.target.value)}
							className="pl-9"
							data-testid="model-limits-search-input"
						/>
					</div>
				</div>

				<div className="rounded-sm border" data-testid="model-limits-table">
					<Table>
						<TableHeader>
							<TableRow className="hover:bg-transparent">
								<TableHead className="font-medium">Model</TableHead>
								<TableHead className="font-medium">Provider</TableHead>
								<TableHead className="font-medium">Budget</TableHead>
								<TableHead className="font-medium">Rate Limit</TableHead>
								<TableHead className="w-[100px]"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{modelConfigs.length === 0 ? (
								<TableRow>
									<TableCell colSpan={5} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching model limits found.</span>
									</TableCell>
								</TableRow>
							) : (
								modelConfigs.map((config) => {
									const isBudgetExhausted =
										config.budget?.max_limit && config.budget.max_limit > 0 && config.budget.current_usage >= config.budget.max_limit;
									const isRateLimitExhausted =
										(config.rate_limit?.token_max_limit &&
											config.rate_limit.token_max_limit > 0 &&
											config.rate_limit.token_current_usage >= config.rate_limit.token_max_limit) ||
										(config.rate_limit?.request_max_limit &&
											config.rate_limit.request_max_limit > 0 &&
											config.rate_limit.request_current_usage >= config.rate_limit.request_max_limit);
									const isExhausted = isBudgetExhausted || isRateLimitExhausted;

									// Compute safe percentages to avoid division by zero
									const budgetPercentage =
										config.budget?.max_limit && config.budget.max_limit > 0
											? Math.min((config.budget.current_usage / config.budget.max_limit) * 100, 100)
											: 0;
									const tokenPercentage =
										config.rate_limit?.token_max_limit && config.rate_limit.token_max_limit > 0
											? Math.min((config.rate_limit.token_current_usage / config.rate_limit.token_max_limit) * 100, 100)
											: 0;
									const requestPercentage =
										config.rate_limit?.request_max_limit && config.rate_limit.request_max_limit > 0
											? Math.min((config.rate_limit.request_current_usage / config.rate_limit.request_max_limit) * 100, 100)
											: 0;

									return (
										<TableRow
											key={config.id}
											data-testid={`model-limit-row-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
											className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}
										>
											<TableCell className="max-w-[280px] py-4">
												<div className="flex flex-col gap-2">
													<span className="truncate font-mono text-sm font-medium">{config.model_name}</span>
													{isExhausted && (
														<Badge variant="destructive" className="w-fit text-xs">
															Limit Reached
														</Badge>
													)}
												</div>
											</TableCell>
											<TableCell>
												{config.provider ? (
													<div className="flex items-center gap-2">
														<RenderProviderIcon provider={config.provider as ProviderIconType} size="sm" className="h-4 w-4" />
														<span className="text-sm">{ProviderLabels[config.provider as ProviderName] || config.provider}</span>
													</div>
												) : (
													<span className="text-muted-foreground text-sm">All Providers</span>
												)}
											</TableCell>
											<TableCell className="min-w-[200px]">
												{config.budget ? (
													<>
														<TooltipProvider>
															<Tooltip>
																<TooltipTrigger asChild>
																	<div className="space-y-2">
																		<div className="flex items-center justify-between gap-4">
																			<span className="font-medium">{formatCurrency(config.budget.max_limit)}</span>
																			<span className="text-muted-foreground text-xs">
																				{formatResetDuration(config.budget.reset_duration)}
																			</span>
																		</div>
																		<Progress
																			value={budgetPercentage}
																			className={cn(
																				"bg-muted/70 dark:bg-muted/30 h-1.5",
																				isBudgetExhausted
																					? "[&>div]:bg-red-500/70"
																					: budgetPercentage > 80
																						? "[&>div]:bg-amber-500/70"
																						: "[&>div]:bg-emerald-500/70",
																			)}
																		/>
																	</div>
																</TooltipTrigger>
																<TooltipContent>
																	<p className="font-medium">
																		{formatCurrency(config.budget.current_usage)} / {formatCurrency(config.budget.max_limit)}
																	</p>
																	<p className="text-primary-foreground/80 text-xs">
																		Resets {formatResetDuration(config.budget.reset_duration)}
																	</p>
																</TooltipContent>
															</Tooltip>
														</TooltipProvider>
														<ActiveExtensionBadge budgetId={config.budget.id} />
													</>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell className="min-w-[180px]">
												{config.rate_limit ? (
													<div className="space-y-2.5">
														{config.rate_limit.token_max_limit && (
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<div className="space-y-1.5">
																			<div className="flex items-center justify-between gap-4 text-xs">
																				<span className="font-medium">{config.rate_limit.token_max_limit.toLocaleString()} tokens</span>
																				<span className="text-muted-foreground">
																					{formatResetDuration(config.rate_limit.token_reset_duration || "1h")}
																				</span>
																			</div>
																			<Progress
																				value={tokenPercentage}
																				className={cn(
																					"bg-muted/70 dark:bg-muted/30 h-1",
																					config.rate_limit.token_current_usage >= config.rate_limit.token_max_limit
																						? "[&>div]:bg-red-500/70"
																						: tokenPercentage > 80
																							? "[&>div]:bg-amber-500/70"
																							: "[&>div]:bg-emerald-500/70",
																				)}
																			/>
																		</div>
																	</TooltipTrigger>
																	<TooltipContent>
																		<p className="font-medium">
																			{config.rate_limit.token_current_usage.toLocaleString()} /{" "}
																			{config.rate_limit.token_max_limit.toLocaleString()} tokens
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			Resets {formatResetDuration(config.rate_limit.token_reset_duration || "1h")}
																		</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														)}
														{config.rate_limit.request_max_limit && (
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<div className="space-y-1.5">
																			<div className="flex items-center justify-between gap-4 text-xs">
																				<span className="font-medium">{config.rate_limit.request_max_limit.toLocaleString()} req</span>
																				<span className="text-muted-foreground">
																					{formatResetDuration(config.rate_limit.request_reset_duration || "1h")}
																				</span>
																			</div>
																			<Progress
																				value={requestPercentage}
																				className={cn(
																					"bg-muted/70 dark:bg-muted/30 h-1",
																					config.rate_limit.request_current_usage >= config.rate_limit.request_max_limit
																						? "[&>div]:bg-red-500/70"
																						: requestPercentage > 80
																							? "[&>div]:bg-amber-500/70"
																							: "[&>div]:bg-emerald-500/70",
																				)}
																			/>
																		</div>
																	</TooltipTrigger>
																	<TooltipContent>
																		<p className="font-medium">
																			{config.rate_limit.request_current_usage.toLocaleString()} /{" "}
																			{config.rate_limit.request_max_limit.toLocaleString()} requests
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			Resets {formatResetDuration(config.rate_limit.request_reset_duration || "1h")}
																		</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														)}
													</div>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center justify-end gap-1 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100">
													<Button
														variant="ghost"
														size="icon"
														className="h-8 w-8"
														onClick={(e) => {
															e.stopPropagation();
															setExtendBudgetModelConfigId(config.id);
														}}
														disabled={!config.budget || !hasUpdateAccess}
														aria-label={`Extend budget for ${config.model_name}`}
														data-testid={`model-limit-button-extend-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
													>
														<TrendingUp className="h-4 w-4" />
													</Button>
													<Button
														variant="ghost"
														size="icon"
														className="h-8 w-8"
														onClick={(e) => handleEditModelLimit(config, e)}
														disabled={!hasUpdateAccess}
														aria-label={`Edit model limit for ${config.model_name}`}
														data-testid={`model-limit-button-edit-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
													>
														<Edit className="h-4 w-4" />
													</Button>
													<AlertDialog>
														<AlertDialogTrigger asChild>
															<Button
																variant="ghost"
																size="icon"
																className="h-8 w-8 text-red-500 hover:bg-red-500/10 hover:text-red-500"
																onClick={(e) => e.stopPropagation()}
																disabled={!hasDeleteAccess}
																aria-label={`Delete model limit for ${config.model_name}`}
																data-testid={`model-limit-button-delete-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
															>
																<Trash2 className="h-4 w-4" />
															</Button>
														</AlertDialogTrigger>
														<AlertDialogContent>
															<AlertDialogHeader>
																<AlertDialogTitle>Delete Model Limit</AlertDialogTitle>
																<AlertDialogDescription>
																	Are you sure you want to delete the limit for &quot;
																	{config.model_name.length > 30 ? `${config.model_name.slice(0, 30)}...` : config.model_name}
																	&quot;? This action cannot be undone.
																</AlertDialogDescription>
															</AlertDialogHeader>
															<AlertDialogFooter>
																<AlertDialogCancel>Cancel</AlertDialogCancel>
																<AlertDialogAction
																	onClick={() => handleDelete(config.id)}
																	disabled={isDeleting}
																	className="bg-red-600 hover:bg-red-700"
																>
																	{isDeleting ? "Deleting..." : "Delete"}
																</AlertDialogAction>
															</AlertDialogFooter>
														</AlertDialogContent>
													</AlertDialog>
												</div>
											</TableCell>
										</TableRow>
									);
								})
							)}
						</TableBody>
					</Table>
				</div>

				{/* Pagination */}
				{totalCount > 0 && (
					<div className="flex items-center justify-between px-2">
						<p className="text-muted-foreground text-sm">
							Showing {offset + 1}-{Math.min(offset + limit, totalCount)} of {totalCount}
						</p>
						<div className="flex gap-2">
							<Button
								variant="outline"
								size="sm"
								disabled={offset === 0}
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								data-testid="model-limits-pagination-prev-btn"
							>
								<ChevronLeft className="mr-1 h-4 w-4" />
								Previous
							</Button>
							<Button
								variant="outline"
								size="sm"
								disabled={offset + limit >= totalCount}
								onClick={() => onOffsetChange(offset + limit)}
								data-testid="model-limits-pagination-next-btn"
							>
								Next
								<ChevronRight className="ml-1 h-4 w-4" />
							</Button>
						</div>
					</div>
				)}
			</div>
		</>
	);
}