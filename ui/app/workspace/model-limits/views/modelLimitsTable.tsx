"use client";

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
import { Progress } from "@/components/ui/progress";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { resetDurationLabels } from "@/lib/constants/governance";
import { getErrorMessage, useDeleteModelConfigMutation } from "@/lib/store";
import { ModelConfig } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Edit, Gauge, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import ModelLimitSheet from "./modelLimitSheet";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

interface ModelLimitsTableProps {
	modelConfigs: ModelConfig[];
	onRefresh: () => void;
}

export default function ModelLimitsTable({ modelConfigs, onRefresh }: ModelLimitsTableProps) {
	const [showModelLimitSheet, setShowModelLimitSheet] = useState(false);
	const [editingModelConfig, setEditingModelConfig] = useState<ModelConfig | null>(null);

	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const [deleteModelConfig, { isLoading: isDeleting }] = useDeleteModelConfigMutation();

	const handleDelete = async (id: string) => {
		try {
			await deleteModelConfig(id).unwrap();
			toast.success("Model limit deleted successfully");
			onRefresh();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddModelLimit = () => {
		setEditingModelConfig(null);
		setShowModelLimitSheet(true);
	};

	const handleEditModelLimit = (config: ModelConfig, e: React.MouseEvent) => {
		e.stopPropagation();
		setEditingModelConfig(config);
		setShowModelLimitSheet(true);
	};

	const handleModelLimitSaved = () => {
		setShowModelLimitSheet(false);
		setEditingModelConfig(null);
		onRefresh();
	};

	return (
		<>
			{showModelLimitSheet && (
				<ModelLimitSheet modelConfig={editingModelConfig} onSave={handleModelLimitSaved} onCancel={() => setShowModelLimitSheet(false)} />
			)}

			<div className="space-y-6">
				{/* Header */}
				<div className="flex items-start justify-between gap-4">
					<div className="space-y-1">
						<p className="text-muted-foreground text-sm">
							Configure budgets and rate limits at the model level. For provider-specific limits, visit each provider&apos;s settings.
						</p>
					</div>
					<Button onClick={handleAddModelLimit} disabled={!hasCreateAccess} className="shrink-0">
						<Plus className="mr-1.5 h-4 w-4" />
						Add Model Limit
					</Button>
				</div>

				{/* Table or Empty State */}
				{modelConfigs?.length === 0 ? (
					<div className="border-border/60 from-muted/30 to-muted/10 relative overflow-hidden rounded-xl border border-dashed bg-gradient-to-br px-8 py-12">
						<div className="relative flex flex-col items-center text-center">
							{/* Icon */}
							<div className="bg-muted mb-4 rounded-full p-4">
								<Gauge className="text-muted-foreground h-8 w-8" />
							</div>

							{/* Content */}
							<h3 className="mb-2 text-lg font-semibold">No Model Limits Configured</h3>
							<p className="text-muted-foreground mb-6 max-w-md text-sm">
								Add model-level limits to control spending and request rates across your infrastructure.
							</p>

							{/* CTA */}
							<Button onClick={handleAddModelLimit} disabled={!hasCreateAccess} size="lg" className="gap-2">
								<Plus className="h-4 w-4" />
								Add Your First Model Limit
							</Button>

							{/* Feature hints */}
							<div className="text-muted-foreground mt-8 flex items-center justify-center gap-2 text-xs">
								<span>Budget Controls</span>
								<span>•</span>
								<span>Token Limits</span>
								<span>•</span>
								<span>Request Throttling</span>
							</div>
						</div>
						{/* Decorative elements */}
						<div className="bg-primary/5 pointer-events-none absolute -top-8 -right-8 h-32 w-32 rounded-full blur-3xl" />
						<div className="bg-primary/5 pointer-events-none absolute -bottom-8 -left-8 h-32 w-32 rounded-full blur-3xl" />
					</div>
				) : (
					<div className="rounded-lg border">
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
								{modelConfigs?.map((config) => {
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
										<TableRow key={config.id} className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}>
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
											<TableCell className="min-w-[180px]">
												{config.budget ? (
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
												) : (
													<span className="text-muted-foreground text-sm">—</span>
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
													<span className="text-muted-foreground text-sm">—</span>
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center justify-end gap-1 opacity-0 transition-opacity group-hover:opacity-100">
													<TooltipProvider>
														<Tooltip>
															<TooltipTrigger asChild>
																<Button
																	variant="ghost"
																	size="icon"
																	className="h-8 w-8"
																	onClick={(e) => handleEditModelLimit(config, e)}
																	disabled={!hasUpdateAccess}
																>
																	<Edit className="h-4 w-4" />
																</Button>
															</TooltipTrigger>
															<TooltipContent>Edit</TooltipContent>
														</Tooltip>
													</TooltipProvider>
													<AlertDialog>
														<TooltipProvider>
															<Tooltip>
																<AlertDialogTrigger asChild>
																	<TooltipTrigger asChild>
																		<Button
																			variant="ghost"
																			size="icon"
																			className="h-8 w-8 text-red-500 hover:bg-red-500/10 hover:text-red-500"
																			onClick={(e) => e.stopPropagation()}
																			disabled={!hasDeleteAccess}
																		>
																			<Trash2 className="h-4 w-4" />
																		</Button>
																	</TooltipTrigger>
																</AlertDialogTrigger>
																<TooltipContent>Delete</TooltipContent>
															</Tooltip>
														</TooltipProvider>
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
								})}
							</TableBody>
						</Table>
					</div>
				)}
			</div>
		</>
	);
}
