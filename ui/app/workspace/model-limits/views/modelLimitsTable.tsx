import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels, supportsCalendarAlignment } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getModelLimitScope, getModelLimitScopes } from "@/lib/registries/modelLimitScopes";
import { getErrorMessage, useDeleteModelConfigMutation } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { ModelConfig } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { getScopeLabel } from "@/lib/utils/labels";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ArrowUpRight, ChevronLeft, ChevronRight, Edit, MoreHorizontal, Plus, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import ModelLimitSheet from "./modelLimitSheet";
import { ModelLimitsEmptyState } from "./modelLimitsEmptyState";
// Side-effect import: pull in downstream scope registrations (enterprise
// "user" deep-link, etc.). No-op for OSS builds.
import "@enterprise/lib/registrations/modelLimitScopes";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { useNavigate } from "@tanstack/react-router";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

const toTestIdPart = (value: string) =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "");

function ModelLimitActionsMenu({
	config,
	hasUpdateAccess,
	hasDeleteAccess,
	onEdit,
	onDelete,
}: {
	config: ModelConfig;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	onEdit: (config: ModelConfig) => void;
	onDelete: (configId: string) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label={`Actions for model limit ${config.model_name}`}
					data-testid={`model-limit-button-actions-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					className="cursor-pointer"
					disabled={!hasUpdateAccess}
					data-testid={`model-limit-button-edit-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
					onSelect={(e) => {
						e.preventDefault();
						onEdit(config);
						setIsOpen(false);
					}}
				>
					<Edit className="h-4 w-4" />
					Edit
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					disabled={!hasDeleteAccess}
					data-testid={`model-limit-button-delete-${toTestIdPart(config.model_name)}-${toTestIdPart(config.provider || "all")}`}
					onSelect={(e) => {
						e.preventDefault();
						onDelete(config.id);
						setIsOpen(false);
					}}
				>
					<Trash2 className="h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface ModelLimitsTableProps {
	modelConfigs: ModelConfig[];
	totalCount: number;
	providers: ModelProvider[];
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	scope: string;
	onScopeChange: (value: string) => void;
	provider: string;
	onProviderChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
	isLoading?: boolean;
}

export default function ModelLimitsTable({
	modelConfigs,
	totalCount,
	providers,
	search,
	debouncedSearch,
	onSearchChange,
	scope,
	onScopeChange,
	provider,
	onProviderChange,
	offset,
	limit,
	onOffsetChange,
	isLoading = false,
}: ModelLimitsTableProps) {
	const navigate = useNavigate();
	const [showModelLimitSheet, setShowModelLimitSheet] = useState(false);
	const [editingModelConfigId, setEditingModelConfigId] = useState<string | null>(null);
	const [deleteModelConfigId, setDeleteModelConfigId] = useState<string | null>(null);

	// Derive editingModelConfig from props so it stays in sync with RTK cache updates
	const editingModelConfig = useMemo(
		() => (editingModelConfigId ? (modelConfigs.find((mc) => mc.id === editingModelConfigId) ?? null) : null),
		[editingModelConfigId, modelConfigs],
	);
	const deletingModelConfig = useMemo(
		() => (deleteModelConfigId ? (modelConfigs.find((mc) => mc.id === deleteModelConfigId) ?? null) : null),
		[deleteModelConfigId, modelConfigs],
	);

	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const [deleteModelConfig, { isLoading: isDeleting }] = useDeleteModelConfigMutation();

	const handleDelete = async (id: string) => {
		try {
			await deleteModelConfig(id).unwrap();
			toast.success("Model limit deleted successfully");
			setDeleteModelConfigId(null);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddModelLimit = () => {
		setEditingModelConfigId(null);
		setShowModelLimitSheet(true);
	};

	const handleEditModelLimit = (config: ModelConfig) => {
		setEditingModelConfigId(config.id);
		setShowModelLimitSheet(true);
	};

	const handleModelLimitSaved = () => {
		setShowModelLimitSheet(false);
		setEditingModelConfigId(null);
	};

	const hasActiveFilters = debouncedSearch || scope || provider;

	// True empty state: no model limits at all (not just filtered to zero).
	// Suppress while the initial load is in flight so we don't flash the empty
	// state before the API responds.
	if (totalCount === 0 && !hasActiveFilters && !isLoading) {
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
			<AlertDialog open={!!deletingModelConfig} onOpenChange={(open) => !open && setDeleteModelConfigId(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Model Limit</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete the limit for &quot;
							{deletingModelConfig?.model_name && deletingModelConfig.model_name.length > 30
								? `${deletingModelConfig.model_name.slice(0, 30)}...`
								: deletingModelConfig?.model_name}
							&quot;? This action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => deletingModelConfig && handleDelete(deletingModelConfig.id)}
							disabled={isDeleting}
							className="bg-red-600 hover:bg-red-700"
						>
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<div className="flex flex-col overflow-y-auto">
				<div className="mb-4 flex items-center justify-between">
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

				{/* Toolbar: Search + Filters */}
				<div className="mb-4 flex flex-wrap items-center gap-3">
					<div className="relative min-w-[220px] flex-1">
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

					<Select value={scope || "all"} onValueChange={(v) => onScopeChange(v === "all" ? "" : v)}>
						<SelectTrigger className="w-[160px]" data-testid="model-limits-filter-scope">
							<SelectValue placeholder="All Scopes" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="all">All Scopes</SelectItem>
							{getModelLimitScopes().map((o) => (
								<SelectItem key={o.value} value={o.value}>
									{o.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>

					<Select value={provider || "all"} onValueChange={(v) => onProviderChange(v === "all" ? "" : v)}>
						<SelectTrigger className="w-[160px]" data-testid="model-limits-filter-provider">
							<SelectValue placeholder="All Providers" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="all">All Providers</SelectItem>
							{(providers ?? []).map((p) => (
								<SelectItem key={p.name} value={p.name}>
									<div className="flex items-center gap-2">
										<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
										<span>{ProviderLabels[p.name as ProviderName] || p.name}</span>
									</div>
								</SelectItem>
							))}
						</SelectContent>
					</Select>

					{hasActiveFilters && (
						<Button
							variant="ghost"
							size="sm"
							onClick={() => {
								onSearchChange("");
								onScopeChange("");
								onProviderChange("");
							}}
							data-testid="model-limits-filter-clear"
						>
							Clear filters
						</Button>
					)}
				</div>

				<div className="mb-2 overflow-hidden rounded-sm border" data-testid="model-limits-table">
					<Table containerClassName="h-full overflow-auto">
						<TableHeader className="bg-muted sticky top-0 z-10">
							<TableRow className="hover:bg-transparent">
								<TableHead className="font-medium">Model</TableHead>
								<TableHead className="font-medium">Provider</TableHead>
								<TableHead className="font-medium">Scope</TableHead>
								<TableHead className="font-medium">Scope Target</TableHead>
								<TableHead className="font-medium">Budget</TableHead>
								<TableHead className="font-medium">Rate Limit</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-30 w-[50px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{modelConfigs.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">
											{isLoading ? "Loading model limits..." : "No matching model limits found."}
										</span>
									</TableCell>
								</TableRow>
							) : (
								modelConfigs.map((config) => {
									// Model configs can own multiple budgets; show all (like the VK table).
									const budgets = config.budgets ?? (config.budget ? [config.budget] : []);
									const isBudgetExhausted = budgets.some((b) => b.max_limit > 0 && b.current_usage >= b.max_limit);
									const isRateLimitExhausted =
										(config.rate_limit?.token_max_limit &&
											config.rate_limit.token_max_limit > 0 &&
											config.rate_limit.token_current_usage >= config.rate_limit.token_max_limit) ||
										(config.rate_limit?.request_max_limit &&
											config.rate_limit.request_max_limit > 0 &&
											config.rate_limit.request_current_usage >= config.rate_limit.request_max_limit);
									const isExhausted = isBudgetExhausted || isRateLimitExhausted;

									// Compute safe percentages to avoid division by zero
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
													<span className="truncate font-mono text-sm font-medium">
														{config.model_name === "*" ? "All Models" : config.model_name}
													</span>
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
											<TableCell>
												<Badge variant="secondary">{getScopeLabel(config.scope ?? "global")}</Badge>
											</TableCell>
											<TableCell>
												{config.scope !== "global" && config.scope_id && config.scope_name ? (
													<TooltipProvider>
														<Tooltip>
															<TooltipTrigger asChild>
																<Badge
																	variant="secondary"
																	className="flex max-w-[160px] cursor-pointer items-center gap-1 hover:opacity-80"
																	data-testid={`model-limit-scope-target-${config.scope_id}`}
																	onClick={() => {
																		if (!config.scope_id) return;
																		const target = getModelLimitScope(config.scope ?? "global")?.buildDeepLink?.(config.scope_id);
																		if (target) navigate(target as never);
																	}}
																>
																	<span className="truncate">{config.scope_name}</span>
																	<ArrowUpRight className="h-3 w-3 shrink-0" />
																</Badge>
															</TooltipTrigger>
															<TooltipContent className="max-w-[320px] break-all">{config.scope_name}</TooltipContent>
														</Tooltip>
													</TooltipProvider>
												) : (
													<span className="text-muted-foreground text-sm">—</span>
												)}
											</TableCell>
											<TableCell className="min-w-[180px]">
												{budgets.length > 0 ? (
													<div className="flex flex-col gap-1">
														{budgets.map((b, idx) => (
															<div key={b.id ?? idx} className="flex flex-col">
																<span
																	className={cn("font-mono text-sm", b.max_limit > 0 && b.current_usage >= b.max_limit && "text-red-400")}
																>
																	{formatCurrency(b.current_usage)} / {formatCurrency(b.max_limit)}
																</span>
																<span className="text-muted-foreground text-xs">
																	Resets {formatResetDuration(b.reset_duration)}
																	{config.calendar_aligned && supportsCalendarAlignment(b.reset_duration) && " (calendar)"}
																</span>
															</div>
														))}
													</div>
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
											<TableCell
												className={cn(
													"group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right",
													PIN_SHADOW_RIGHT,
												)}
												onClick={(e) => e.stopPropagation()}
											>
												<div className="flex items-center justify-center">
													<ModelLimitActionsMenu
														config={config}
														hasUpdateAccess={hasUpdateAccess}
														hasDeleteAccess={hasDeleteAccess}
														onEdit={handleEditModelLimit}
														onDelete={setDeleteModelConfigId}
													/>
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
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{(offset + 1).toLocaleString()}-{Math.min(offset + limit, totalCount).toLocaleString()} of {totalCount.toLocaleString()}{" "}
							entries
						</div>

						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="model-limits-pagination-prev-btn"
								aria-label="Previous page"
							>
								<ChevronLeft className="size-3" />
							</Button>

							<div className="flex items-center gap-1">
								<span>Page</span>
								<span>{Math.floor(offset / limit) + 1}</span>
								<span>of {Math.ceil(totalCount / limit)}</span>
							</div>

							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="model-limits-pagination-next-btn"
								aria-label="Next page"
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>
		</>
	);
}