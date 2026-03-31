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
import { Checkbox } from "@/components/ui/checkbox";
import { Progress } from "@/components/ui/progress";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels } from "@/lib/constants/governance";
import { getErrorMessage, useDeleteTeamMutation } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Input } from "@/components/ui/input";
import { ChevronLeft, ChevronRight, Edit, Plus, Search, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import TeamDialog from "./teamDialog";
import { TeamsEmptyState } from "./teamsEmptyState";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

const setHasSameValues = (left: Set<string>, right: Set<string>) => left.size === right.size && Array.from(left).every((value) => right.has(value));

interface TeamsTableProps {
	teams: Team[];
	totalCount: number;
	customers: Customer[];
	virtualKeys: VirtualKey[];
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function TeamsTable({ teams, totalCount, customers, virtualKeys, search, debouncedSearch, onSearchChange, offset, limit, onOffsetChange }: TeamsTableProps) {
	const [showTeamDialog, setShowTeamDialog] = useState(false);
	const [editingTeam, setEditingTeam] = useState<Team | null>(null);
	const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
	const [showBulkDeleteDialog, setShowBulkDeleteDialog] = useState(false);
	const [isBulkDeleting, setIsBulkDeleting] = useState(false);

	const hasCreateAccess = useRbac(RbacResource.Teams, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Teams, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Teams, RbacOperation.Delete);

	const [deleteTeam, { isLoading: isDeleting }] = useDeleteTeamMutation();

	const handleDelete = async (teamId: string) => {
		try {
			await deleteTeam(teamId).unwrap();
			toast.success("Team deleted successfully");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const toggleRowSelection = (teamId: string) => {
		setSelectedIds((previous) => {
			const next = new Set(previous);
			if (next.has(teamId)) {
				next.delete(teamId);
			} else {
				next.add(teamId);
			}
			return next;
		});
	};

	const toggleSelectAll = () => {
		if (selectedIds.size === teams.length && teams.length > 0) {
			setSelectedIds(new Set());
			return;
		}
		setSelectedIds(new Set(teams.map((team) => team.id)));
	};

	const handleBulkDelete = async () => {
		if (isBulkDeleting || selectedIds.size === 0) return;

		setIsBulkDeleting(true);
		try {
			const teamIds = Array.from(selectedIds);
			let deletedCount = 0;
			const failedIds: string[] = [];

			for (const teamId of teamIds) {
				try {
					await deleteTeam(teamId).unwrap();
					deletedCount += 1;
				} catch {
					failedIds.push(teamId);
				}
			}

			if (deletedCount > 0) {
				toast.success(`${deletedCount} team(s) deleted successfully`);
			}

			if (failedIds.length > 0) {
				toast.error(
					deletedCount > 0
						? `${failedIds.length} team(s) could not be deleted.`
						: "Failed to delete the selected teams.",
				);
				setSelectedIds(new Set(failedIds));
				return;
			}

			setSelectedIds(new Set());
			setShowBulkDeleteDialog(false);
		} finally {
			setIsBulkDeleting(false);
		}
	};

	const handleAddTeam = () => {
		setEditingTeam(null);
		setShowTeamDialog(true);
	};

	const handleEditTeam = (team: Team) => {
		setEditingTeam(team);
		setShowTeamDialog(true);
	};

	const handleTeamSaved = () => {
		setShowTeamDialog(false);
		setEditingTeam(null);
	};

	const getVirtualKeysForTeam = (teamId: string) => {
		return virtualKeys.filter((vk) => vk.team_id === teamId);
	};

	const getCustomerName = (customerId?: string) => {
		if (!customerId) return "-";
		const customer = customers.find((c) => c.id === customerId);
		return customer ? customer.name : "Unknown Customer";
	};

	const hasActiveFilters = debouncedSearch;
	const isAllSelected = selectedIds.size === teams.length && teams.length > 0;

	useEffect(() => {
		const visibleIDs = new Set(teams.map((team) => team.id));
		setSelectedIds((previous) => {
			const next = new Set(Array.from(previous).filter((id) => visibleIDs.has(id)));
			return setHasSameValues(previous, next) ? previous : next;
		});
	}, [teams]);

	// True empty state: no teams at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				<TooltipProvider>
					{showTeamDialog && (
						<TeamDialog team={editingTeam} customers={customers} onSave={handleTeamSaved} onCancel={() => setShowTeamDialog(false)} />
					)}
					<TeamsEmptyState onAddClick={handleAddTeam} canCreate={hasCreateAccess} />
				</TooltipProvider>
			</>
		);
	}

	return (
		<>
			<TooltipProvider>
				{showTeamDialog && (
					<TeamDialog team={editingTeam} customers={customers} onSave={handleTeamSaved} onCancel={() => setShowTeamDialog(false)} />
				)}

				<div className="space-y-4">
					<div className="flex items-center justify-between">
						<div>
							<h2 className="text-lg font-semibold">Teams</h2>
							<p className="text-muted-foreground text-sm">Organize users into teams with shared budgets and access controls.</p>
						</div>
						<Button data-testid="create-team-btn" onClick={handleAddTeam} disabled={!hasCreateAccess}>
							<Plus className="h-4 w-4" />
							Add Team
						</Button>
					</div>

					{selectedIds.size > 0 && (
						<div className="flex items-center justify-between rounded-md border border-border bg-secondary px-4 py-3">
							<span className="text-sm font-medium">
								{selectedIds.size} team{selectedIds.size !== 1 ? "s" : ""} selected
							</span>
							<AlertDialog
								open={showBulkDeleteDialog}
								onOpenChange={(open) => {
									if (!open && isBulkDeleting) return;
									setShowBulkDeleteDialog(open);
								}}
							>
								<AlertDialogTrigger asChild>
									<Button
										variant="destructive"
										size="sm"
										disabled={!hasDeleteAccess || isBulkDeleting}
										data-testid="teams-bulk-delete-btn"
									>
										<Trash2 className="mr-2 h-4 w-4" />
										Delete
									</Button>
								</AlertDialogTrigger>
								<AlertDialogContent>
									<AlertDialogHeader>
										<AlertDialogTitle>Delete Teams</AlertDialogTitle>
										<AlertDialogDescription>
											Are you sure you want to delete {selectedIds.size} team{selectedIds.size !== 1 ? "s" : ""}? This action cannot be undone.
										</AlertDialogDescription>
									</AlertDialogHeader>
									<AlertDialogFooter>
										<AlertDialogCancel disabled={isBulkDeleting}>Cancel</AlertDialogCancel>
										<AlertDialogAction
											onClick={(event) => {
												event.preventDefault();
												void handleBulkDelete();
											}}
											disabled={isBulkDeleting}
											className="bg-red-600 hover:bg-red-700"
											data-testid="teams-confirm-bulk-delete-btn"
										>
											{isBulkDeleting ? "Deleting..." : "Delete"}
										</AlertDialogAction>
									</AlertDialogFooter>
								</AlertDialogContent>
							</AlertDialog>
						</div>
					)}

					<div className="flex items-center gap-3">
						<div className="relative max-w-sm flex-1">
							<Search className="text-muted-foreground absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2" />
							<Input
								aria-label="Search teams by name"
								placeholder="Search by name..."
								value={search}
								onChange={(e) => onSearchChange(e.target.value)}
								className="pl-9"
								data-testid="teams-search-input"
							/>
						</div>
					</div>

					<div className="rounded-sm border overflow-hidden" data-testid="teams-table">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead className="w-12">
										<Checkbox
											checked={isAllSelected}
											onCheckedChange={toggleSelectAll}
											aria-label="Select all teams"
											data-testid="teams-select-all-checkbox"
										/>
									</TableHead>
									<TableHead>Name</TableHead>
									<TableHead>Customer</TableHead>
									<TableHead>Budget</TableHead>
									<TableHead>Rate Limit</TableHead>
									<TableHead>Virtual Keys</TableHead>
									<TableHead className="text-right"></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{teams.length === 0 ? (
									<TableRow>
										<TableCell colSpan={7} className="h-24 text-center">
											<span className="text-muted-foreground text-sm">No matching teams found.</span>
										</TableCell>
									</TableRow>
								) : (
								teams.map((team) => {
									const vks = getVirtualKeysForTeam(team.id);
									const customerName = getCustomerName(team.customer_id);

									// Budget calculations
									const isBudgetExhausted =
										team.budget?.max_limit && team.budget.max_limit > 0 && team.budget.current_usage >= team.budget.max_limit;
									const budgetPercentage =
										team.budget?.max_limit && team.budget.max_limit > 0
											? Math.min((team.budget.current_usage / team.budget.max_limit) * 100, 100)
											: 0;

									// Rate limit calculations
									const isTokenLimitExhausted =
										team.rate_limit?.token_max_limit &&
										team.rate_limit.token_max_limit > 0 &&
										team.rate_limit.token_current_usage >= team.rate_limit.token_max_limit;
									const isRequestLimitExhausted =
										team.rate_limit?.request_max_limit &&
										team.rate_limit.request_max_limit > 0 &&
										team.rate_limit.request_current_usage >= team.rate_limit.request_max_limit;
									const isRateLimitExhausted = isTokenLimitExhausted || isRequestLimitExhausted;
									const tokenPercentage =
										team.rate_limit?.token_max_limit && team.rate_limit.token_max_limit > 0
											? Math.min((team.rate_limit.token_current_usage / team.rate_limit.token_max_limit) * 100, 100)
											: 0;
									const requestPercentage =
										team.rate_limit?.request_max_limit && team.rate_limit.request_max_limit > 0
											? Math.min((team.rate_limit.request_current_usage / team.rate_limit.request_max_limit) * 100, 100)
											: 0;

									const isExhausted = isBudgetExhausted || isRateLimitExhausted;

									return (
										<TableRow key={team.id} data-testid={`team-row-${team.name}`} className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}>
											<TableCell>
												<Checkbox
													checked={selectedIds.has(team.id)}
													onCheckedChange={() => toggleRowSelection(team.id)}
													aria-label={`Select team ${team.name}`}
													data-testid={`team-checkbox-${team.name}`}
												/>
											</TableCell>
											<TableCell className="max-w-[200px] py-4">
												<div className="flex flex-col gap-2">
													<span className="truncate font-medium">{team.name}</span>
													{isExhausted && (
														<Badge variant="destructive" className="w-fit text-xs">
															Limit Reached
														</Badge>
													)}
												</div>
											</TableCell>
											<TableCell data-testid={`team-row-${team.name}-customer`}>
												<div className="flex items-center gap-2">
													<Badge variant={team.customer_id ? "secondary" : "outline"}>{customerName}</Badge>
												</div>
											</TableCell>
											<TableCell className="min-w-[180px]">
												{team.budget ? (
													<Tooltip>
														<TooltipTrigger asChild>
															<div className="space-y-2">
																<div className="flex items-center justify-between gap-4">
																	<span className="font-medium">{formatCurrency(team.budget.max_limit)}</span>
																	<span className="text-muted-foreground text-xs">{formatResetDuration(team.budget.reset_duration)}</span>
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
																{formatCurrency(team.budget.current_usage)} / {formatCurrency(team.budget.max_limit)}
															</p>
															<p className="text-primary-foreground/80 text-xs">Resets {formatResetDuration(team.budget.reset_duration)}</p>
														</TooltipContent>
													</Tooltip>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell className="min-w-[180px]">
												{team.rate_limit ? (
													<div className="space-y-2.5">
														{team.rate_limit.token_max_limit && (
															<Tooltip>
																<TooltipTrigger asChild>
																	<div className="space-y-1.5">
																		<div className="flex items-center justify-between gap-4 text-xs">
																			<span className="font-medium">{team.rate_limit.token_max_limit.toLocaleString()} tokens</span>
																			<span className="text-muted-foreground">
																				{formatResetDuration(team.rate_limit.token_reset_duration || "1h")}
																			</span>
																		</div>
																		<Progress
																			value={tokenPercentage}
																			className={cn(
																				"bg-muted/70 dark:bg-muted/30 h-1",
																				isTokenLimitExhausted
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
																		{team.rate_limit.token_current_usage.toLocaleString()} /{" "}
																		{team.rate_limit.token_max_limit.toLocaleString()} tokens
																	</p>
																	<p className="text-primary-foreground/80 text-xs">
																		Resets {formatResetDuration(team.rate_limit.token_reset_duration || "1h")}
																	</p>
																</TooltipContent>
															</Tooltip>
														)}
														{team.rate_limit.request_max_limit && (
															<Tooltip>
																<TooltipTrigger asChild>
																	<div className="space-y-1.5">
																		<div className="flex items-center justify-between gap-4 text-xs">
																			<span className="font-medium">{team.rate_limit.request_max_limit.toLocaleString()} req</span>
																			<span className="text-muted-foreground">
																				{formatResetDuration(team.rate_limit.request_reset_duration || "1h")}
																			</span>
																		</div>
																		<Progress
																			value={requestPercentage}
																			className={cn(
																				"bg-muted/70 dark:bg-muted/30 h-1",
																				isRequestLimitExhausted
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
																		{team.rate_limit.request_current_usage.toLocaleString()} /{" "}
																		{team.rate_limit.request_max_limit.toLocaleString()} requests
																	</p>
																	<p className="text-primary-foreground/80 text-xs">
																		Resets {formatResetDuration(team.rate_limit.request_reset_duration || "1h")}
																	</p>
																</TooltipContent>
															</Tooltip>
														)}
													</div>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell>
												{vks.length > 0 ? (
													<div className="flex items-center gap-2">
														<Tooltip>
															<TooltipTrigger>
																<Badge variant="outline" className="text-xs">
																	{vks.length} {vks.length === 1 ? "key" : "keys"}
																</Badge>
															</TooltipTrigger>
															<TooltipContent>{vks.map((vk) => vk.name).join(", ")}</TooltipContent>
														</Tooltip>
													</div>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell className="text-right">
												<div className="flex items-center justify-end gap-1 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100">
													<Button
														variant="ghost"
														size="icon"
														className="h-8 w-8"
														onClick={() => handleEditTeam(team)}
														disabled={!hasUpdateAccess}
														aria-label={`Edit team ${team.name}`}
														data-testid={`team-edit-btn-${team.name}`}
													>
														<Edit className="h-4 w-4" />
													</Button>
													<AlertDialog>
														<AlertDialogTrigger asChild>
															<Button
																variant="ghost"
																size="icon"
																className="h-8 w-8 text-red-500 hover:bg-red-500/10 hover:text-red-500"
																disabled={!hasDeleteAccess}
																aria-label={`Delete team ${team.name}`}
																data-testid={`team-delete-btn-${team.name}`}
															>
																<Trash2 className="h-4 w-4" />
															</Button>
														</AlertDialogTrigger>
														<AlertDialogContent>
															<AlertDialogHeader>
																<AlertDialogTitle>Delete Team</AlertDialogTitle>
																<AlertDialogDescription>
																	Are you sure you want to delete &quot;{team.name}&quot;? This will also unassign any virtual keys from
																	this team. This action cannot be undone.
																</AlertDialogDescription>
															</AlertDialogHeader>
															<AlertDialogFooter>
																<AlertDialogCancel>Cancel</AlertDialogCancel>
																<AlertDialogAction
																	onClick={() => handleDelete(team.id)}
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
									data-testid="teams-pagination-prev-btn"
								>
									<ChevronLeft className="mr-1 h-4 w-4" /> Previous
								</Button>
								<Button
									variant="outline"
									size="sm"
									disabled={offset + limit >= totalCount}
									onClick={() => onOffsetChange(offset + limit)}
									data-testid="teams-pagination-next-btn"
								>
									Next <ChevronRight className="ml-1 h-4 w-4" />
								</Button>
							</div>
						</div>
					)}
				</div>
			</TooltipProvider>
		</>
	);
}
