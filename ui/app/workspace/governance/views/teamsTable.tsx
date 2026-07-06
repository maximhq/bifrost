import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels } from "@/lib/constants/governance";
import { getErrorMessage, useDeleteTeamMutation } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Edit, MoreHorizontal, Plus, ScrollText, Search, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import TeamSheet from "./teamSheet";
import { TeamsEmptyState } from "./teamsEmptyState";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

function TeamActionsMenu({
	team,
	hasUpdateAccess,
	hasDeleteAccess,
	isDeleting,
	onEdit,
	onDelete,
}: {
	team: Team;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	isDeleting: boolean;
	onEdit: (team: Team) => void;
	onDelete: (teamId: string) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);

	return (
		<>
			<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
				<DropdownMenuTrigger asChild>
					<Button
						variant="ghost"
						size="icon"
						className="h-8 w-8"
						aria-label={`Team actions for ${team.name}`}
						data-testid={`team-actions-btn-${team.name}`}
					>
						<MoreHorizontal className="h-4 w-4" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem
						className="cursor-pointer"
						disabled={!hasUpdateAccess}
						data-testid={`team-edit-btn-${team.name}`}
						onSelect={(e) => {
							e.preventDefault();
							onEdit(team);
							setIsOpen(false);
						}}
					>
						<Edit className="h-4 w-4" />
						Edit
					</DropdownMenuItem>
					<DropdownMenuItem asChild className="cursor-pointer" data-testid={`team-view-logs-btn-${team.name}`}>
						<Link to="/workspace/logs" search={{ team_ids: [team.id] }} onClick={() => setIsOpen(false)}>
							<ScrollText className="h-4 w-4" />
							View logs
						</Link>
					</DropdownMenuItem>
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						disabled={!hasDeleteAccess}
						data-testid={`team-delete-btn-${team.name}`}
						onSelect={(e) => {
							e.preventDefault();
							setDeleteOpen(true);
							setIsOpen(false);
						}}
					>
						<Trash2 className="h-4 w-4" />
						Delete
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
			<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Team</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete &quot;{team.name}&quot;? This will also unassign any virtual keys from this team. This action
							cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={() => onDelete(team.id)} disabled={isDeleting} className="bg-red-600 hover:bg-red-700">
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}

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
	selectedTeamId: string | null;
	onTeamAdd: () => void;
	onTeamSelect: (team: Team | null) => void;
	onDialogClose: () => void;
	isLoading?: boolean;
}

export default function TeamsTable({
	teams,
	totalCount,
	customers,
	virtualKeys,
	search,
	debouncedSearch,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
	selectedTeamId,
	onTeamAdd,
	onTeamSelect,
	onDialogClose,
	isLoading,
}: TeamsTableProps) {
	const showTeamSheet = selectedTeamId !== null && selectedTeamId !== "";
	const editingTeam = selectedTeamId && selectedTeamId !== "new" ? (teams.find((t) => t.id === selectedTeamId) ?? null) : null;

	// If a team ID is in the URL but can't be resolved (deleted or filtered out),
	// clear it so we don't silently open the dialog in "create" mode.
	useEffect(() => {
		if (selectedTeamId && selectedTeamId !== "new" && !editingTeam) {
			onDialogClose();
		}
	}, [selectedTeamId, editingTeam, onDialogClose]);

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

	const handleAddTeam = () => {
		onTeamAdd();
	};

	const handleEditTeam = (team: Team) => {
		onTeamSelect(team);
	};

	const handleTeamSaved = () => {
		onDialogClose();
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

	// True empty state: no teams at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters && !isLoading) {
		return (
			<>
				<TooltipProvider>
					{showTeamSheet && <TeamSheet team={editingTeam} customers={customers} onSave={handleTeamSaved} onCancel={onDialogClose} />}
					<TeamsEmptyState onAddClick={handleAddTeam} canCreate={hasCreateAccess} />
				</TooltipProvider>
			</>
		);
	}

	return (
		<>
			<TooltipProvider>
				{showTeamSheet && <TeamSheet team={editingTeam} customers={customers} onSave={handleTeamSaved} onCancel={onDialogClose} />}

				<div className="flex grow flex-col overflow-y-auto">
					<div className="mb-4 flex items-center justify-between">
						<div>
							<h2 className="text-lg font-semibold">Teams</h2>
							<p className="text-muted-foreground text-sm">Organize users into teams with shared budgets and access controls.</p>
						</div>
						<Button data-testid="create-team-btn" onClick={handleAddTeam} disabled={!hasCreateAccess}>
							<Plus className="h-4 w-4" />
							Add Team
						</Button>
					</div>

					<div className="mb-4 flex items-center gap-3">
						<div className="relative max-w-sm flex-1">
							<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
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

					<div className="mb-2 grow overflow-auto rounded-sm border" data-testid="teams-table">
						<Table className="min-w-[1100px]" containerClassName="h-full">
							<TableHeader className="bg-background sticky top-0">
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Customer</TableHead>
									<TableHead>Budget</TableHead>
									<TableHead>Rate Limit</TableHead>
									<TableHead>Virtual Keys</TableHead>
									<TableHead className={`bg-muted sticky right-0 z-10 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{teams.length === 0 ? (
									<TableRow>
										<TableCell colSpan={6} className="h-24 text-center">
											<span className="text-muted-foreground text-sm">No matching teams found.</span>
										</TableCell>
									</TableRow>
								) : (
									teams.map((team) => {
										const vks = getVirtualKeysForTeam(team.id);
										const customerName = getCustomerName(team.customer_id);

										// Budget calculations — any of the team's budgets exhausted
										const teamBudgets = team.budgets ?? [];
										const isBudgetExhausted = teamBudgets.some((b) => b.max_limit > 0 && b.current_usage >= b.max_limit);

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
											<TableRow
												key={team.id}
												data-testid={`team-row-${team.name}`}
												className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}
											>
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
													{teamBudgets.length > 0 ? (
														<div className="space-y-2.5">
															{teamBudgets.map((b) => {
																const budgetPercentage = b.max_limit > 0 ? Math.min((b.current_usage / b.max_limit) * 100, 100) : 0;
																const isExhausted = b.max_limit > 0 && b.current_usage >= b.max_limit;
																return (
																	<Tooltip key={b.id}>
																		<TooltipTrigger asChild>
																			<div className="space-y-1.5">
																				<div className="flex items-center justify-between gap-4">
																					<span className="font-medium">{formatCurrency(b.max_limit)}</span>
																					<span className="text-muted-foreground text-xs">{formatResetDuration(b.reset_duration)}</span>
																				</div>
																				<Progress
																					value={budgetPercentage}
																					className={cn(
																						"bg-muted/70 dark:bg-muted/30 h-1.5",
																						isExhausted
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
																				{formatCurrency(b.current_usage)} / {formatCurrency(b.max_limit)}
																			</p>
																			<p className="text-primary-foreground/80 text-xs">Resets {formatResetDuration(b.reset_duration)}</p>
																		</TooltipContent>
																	</Tooltip>
																);
															})}
														</div>
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
												<TableCell
													className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-10 bg-white text-right ${PIN_SHADOW_RIGHT}`}
												>
													<TeamActionsMenu
														team={team}
														hasUpdateAccess={hasUpdateAccess}
														hasDeleteAccess={hasDeleteAccess}
														isDeleting={isDeleting}
														onEdit={handleEditTeam}
														onDelete={handleDelete}
													/>
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
									data-testid="teams-pagination-prev-btn"
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
									data-testid="teams-pagination-next-btn"
									aria-label="Next page"
								>
									<ChevronRight className="size-3" />
								</Button>
							</div>
						</div>
					)}
				</div>
			</TooltipProvider>
		</>
	);
}