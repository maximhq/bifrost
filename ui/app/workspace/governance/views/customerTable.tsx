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
import { getErrorMessage, useDeleteCustomerMutation } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { CustomerDetailSheet } from "@enterprise/components/user-groups/sheets/customerDetailSheet";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Edit, MoreHorizontal, Plus, ScrollText, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { CustomersEmptyState } from "./customersEmptyState";
import CustomerSheet from "./customerSheet";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

const ACTIONS_COLUMN_CLASS = `sticky right-0 z-10 w-[56px] min-w-[56px] text-right ${PIN_SHADOW_RIGHT}`;

interface CustomerActionsMenuProps {
	customer: Customer;
	canUpdate: boolean;
	canDelete: boolean;
	onEdit: (customer: Customer) => void;
	onDelete: (customer: Customer) => void;
}

function CustomerActionsMenu({ customer, canUpdate, canDelete, onEdit, onDelete }: CustomerActionsMenuProps) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label={`Customer actions ${customer.name}`}
					data-testid={`customer-actions-btn-${customer.id}`}
					onClick={(e) => e.stopPropagation()}
					onPointerDown={(e) => e.stopPropagation()}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					disabled={!canUpdate}
					data-testid={`customer-button-edit-${customer.id}`}
					onSelect={(e) => {
						e.stopPropagation();
						e.preventDefault();
						onEdit(customer);
						setIsOpen(false);
					}}
					onClick={(e) => e.stopPropagation()}
					onPointerDown={(e) => e.stopPropagation()}
				>
					<Edit className="h-4 w-4" />
					Edit
				</DropdownMenuItem>
				<DropdownMenuItem asChild className="cursor-pointer" data-testid={`customer-button-view-logs-${customer.id}`}>
					<Link
						to="/workspace/logs"
						search={{ customer_ids: [customer.id] }}
						onClick={(e) => {
							e.stopPropagation();
							setIsOpen(false);
						}}
						onPointerDown={(e) => e.stopPropagation()}
					>
						<ScrollText className="h-4 w-4" />
						View logs
					</Link>
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					disabled={!canDelete}
					data-testid={`customer-button-delete-${customer.id}`}
					onSelect={(e) => {
						e.preventDefault();
						onDelete(customer);
						setIsOpen(false);
					}}
					onClick={(e) => e.stopPropagation()}
					onPointerDown={(e) => e.stopPropagation()}
				>
					<Trash2 className="h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface CustomersTableProps {
	customers: Customer[];
	totalCount: number;
	teams: Team[];
	virtualKeys: VirtualKey[];
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
	isFetching?: boolean;
}

export default function CustomersTable({
	customers,
	totalCount,
	teams,
	virtualKeys,
	search,
	debouncedSearch,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
	isFetching,
}: CustomersTableProps) {
	const [showCustomerSheet, setShowCustomerSheet] = useState(false);
	const [editingCustomer, setEditingCustomer] = useState<Customer | null>(null);
	const [confirmDeleteCustomer, setConfirmDeleteCustomer] = useState<Customer | null>(null);
	const [viewingCustomer, setViewingCustomer] = useState<Customer | null>(null);

	const hasCreateAccess = useRbac(RbacResource.Customers, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Customers, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Customers, RbacOperation.Delete);

	const [deleteCustomer, { isLoading: isDeleting }] = useDeleteCustomerMutation();

	const handleDelete = async (customerId: string) => {
		try {
			await deleteCustomer(customerId).unwrap();
			toast.success("Customer deleted successfully");
		} catch (error) {
			toast.error(getErrorMessage(error));
		} finally {
			setConfirmDeleteCustomer(null);
		}
	};

	const handleAddCustomer = () => {
		setEditingCustomer(null);
		setShowCustomerSheet(true);
	};

	const handleEditCustomer = (customer: Customer) => {
		setEditingCustomer(customer);
		setShowCustomerSheet(true);
	};

	const handleCustomerSaved = () => {
		setShowCustomerSheet(false);
		setEditingCustomer(null);
	};

	const getTeamsForCustomer = (customerId: string) => {
		return teams.filter((team) => team.customer_id === customerId);
	};

	const getVirtualKeysForCustomer = (customerId: string) => {
		return virtualKeys.filter((vk) => vk.customer_id === customerId);
	};

	const hasActiveFilters = debouncedSearch;

	// True empty state: no customers at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters && !isFetching) {
		return (
			<>
				<TooltipProvider>
					<CustomerSheet
						open={showCustomerSheet}
						onOpenChange={(open) => {
							setShowCustomerSheet(open);
							if (!open) setEditingCustomer(null);
						}}
						customer={editingCustomer}
						onSuccess={handleCustomerSaved}
					/>
					<CustomersEmptyState onAddClick={handleAddCustomer} canCreate={hasCreateAccess} />
				</TooltipProvider>
			</>
		);
	}

	return (
		<>
			<TooltipProvider>
				<CustomerSheet
					open={showCustomerSheet}
					onOpenChange={(open) => {
						setShowCustomerSheet(open);
						if (!open) setEditingCustomer(null);
					}}
					customer={editingCustomer}
					onSuccess={handleCustomerSaved}
				/>

				<CustomerDetailSheet
					open={!!viewingCustomer}
					onOpenChange={(open) => {
						if (!open) setViewingCustomer(null);
					}}
					customer={viewingCustomer}
				/>

				<div className="flex grow flex-col">
					<div className="mb-4 flex items-center justify-between">
						<div>
							<h2 className="text-lg font-semibold">Customers</h2>
							<p className="text-muted-foreground text-sm">Manage customer accounts with their own teams, budgets, and access controls.</p>
						</div>
						<Button data-testid="customer-button-create" onClick={handleAddCustomer} disabled={!hasCreateAccess}>
							<Plus className="h-4 w-4" />
							Add Customer
						</Button>
					</div>

					<div className="mb-4 flex items-center gap-3">
						<div className="relative max-w-sm flex-1">
							<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
							<Input
								aria-label="Search customers by name"
								placeholder="Search by name..."
								value={search}
								onChange={(e) => onSearchChange(e.target.value)}
								className="pl-9"
								data-testid="customers-search-input"
							/>
						</div>
					</div>

					<div className="mb-2 grow overflow-auto rounded-sm border" data-testid="customer-table-container">
						<Table className="min-w-[1100px]">
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Teams</TableHead>
									<TableHead>Budget</TableHead>
									<TableHead>Rate Limit</TableHead>
									<TableHead>Virtual Keys</TableHead>
									<TableHead className={`bg-muted ${ACTIONS_COLUMN_CLASS}`}></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{customers.length === 0 ? (
									<TableRow>
										<TableCell colSpan={6} className="h-24 text-center">
											<span className="text-muted-foreground text-sm">No matching customers found.</span>
										</TableCell>
									</TableRow>
								) : (
									customers.map((customer) => {
										const customerTeams = getTeamsForCustomer(customer.id);
										const vks = getVirtualKeysForCustomer(customer.id);

										// Budget calculations (most-exhausted budget drives the row highlight)
										const budgets = customer.budgets ?? [];
										const isBudgetExhausted = budgets.some((b) => b.max_limit > 0 && b.current_usage >= b.max_limit);

										// Rate limit calculations
										const isTokenLimitExhausted =
											customer.rate_limit?.token_max_limit &&
											customer.rate_limit.token_max_limit > 0 &&
											customer.rate_limit.token_current_usage >= customer.rate_limit.token_max_limit;
										const isRequestLimitExhausted =
											customer.rate_limit?.request_max_limit &&
											customer.rate_limit.request_max_limit > 0 &&
											customer.rate_limit.request_current_usage >= customer.rate_limit.request_max_limit;
										const isRateLimitExhausted = isTokenLimitExhausted || isRequestLimitExhausted;
										const tokenPercentage =
											customer.rate_limit?.token_max_limit && customer.rate_limit.token_max_limit > 0
												? Math.min((customer.rate_limit.token_current_usage / customer.rate_limit.token_max_limit) * 100, 100)
												: 0;
										const requestPercentage =
											customer.rate_limit?.request_max_limit && customer.rate_limit.request_max_limit > 0
												? Math.min((customer.rate_limit.request_current_usage / customer.rate_limit.request_max_limit) * 100, 100)
												: 0;

										const isExhausted = isBudgetExhausted || isRateLimitExhausted;

										return (
											<TableRow
												key={customer.id}
												data-testid={`customer-row-${customer.name}`}
												className={cn(
													"group cursor-pointer transition-colors",
													isExhausted ? "bg-red-500/5 hover:bg-red-500/10" : "hover:bg-muted/50",
												)}
												role="button"
												tabIndex={0}
												onClick={() => setViewingCustomer(customer)}
												onKeyDown={(e) => {
													if (e.target !== e.currentTarget) return;
													if (e.key === "Enter" || e.key === " ") {
														e.preventDefault();
														setViewingCustomer(customer);
													}
												}}
											>
												<TableCell className="max-w-[200px] py-4">
													<div className="flex flex-col gap-2">
														<span className="truncate font-medium">{customer.name}</span>
														{isExhausted && (
															<Badge variant="destructive" className="w-fit text-xs">
																Limit Reached
															</Badge>
														)}
													</div>
												</TableCell>
												<TableCell>
													{customerTeams?.length > 0 ? (
														<div className="flex items-center gap-2">
															<Tooltip>
																<TooltipTrigger>
																	<Badge variant="outline" className="text-xs">
																		{customerTeams.length} {customerTeams.length === 1 ? "team" : "teams"}
																	</Badge>
																</TooltipTrigger>
																<TooltipContent>{customerTeams.map((team) => team.name).join(", ")}</TooltipContent>
															</Tooltip>
														</div>
													) : (
														<span className="text-muted-foreground text-sm">-</span>
													)}
												</TableCell>
												<TableCell className="min-w-[180px]">
													{budgets.length > 0 ? (
														<div className="space-y-2">
															{budgets.map((budget) => {
																const pct = budget.max_limit > 0 ? Math.min((budget.current_usage / budget.max_limit) * 100, 100) : 0;
																const exhausted = budget.max_limit > 0 && budget.current_usage >= budget.max_limit;
																return (
																	<Tooltip key={budget.id}>
																		<TooltipTrigger asChild>
																			<div className="space-y-1">
																				<div className="flex items-center justify-between gap-4">
																					<span className="text-sm font-medium">{formatCurrency(budget.max_limit)}</span>
																					<span className="text-muted-foreground text-xs">
																						{formatResetDuration(budget.reset_duration)}
																					</span>
																				</div>
																				<Progress
																					value={pct}
																					className={cn(
																						"bg-muted/70 dark:bg-muted/30 h-1.5",
																						exhausted
																							? "[&>div]:bg-red-500/70"
																							: pct > 80
																								? "[&>div]:bg-amber-500/70"
																								: "[&>div]:bg-emerald-500/70",
																					)}
																				/>
																			</div>
																		</TooltipTrigger>
																		<TooltipContent>
																			<p className="font-medium">
																				{formatCurrency(budget.current_usage)} / {formatCurrency(budget.max_limit)}
																			</p>
																			<p className="text-primary-foreground/80 text-xs">
																				Resets {formatResetDuration(budget.reset_duration)}
																			</p>
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
													{customer.rate_limit ? (
														<div className="space-y-2.5">
															{customer.rate_limit.token_max_limit && (
																<Tooltip>
																	<TooltipTrigger asChild>
																		<div className="space-y-1.5">
																			<div className="flex items-center justify-between gap-4 text-xs">
																				<span className="font-medium">{customer.rate_limit.token_max_limit.toLocaleString()} tokens</span>
																				<span className="text-muted-foreground">
																					{formatResetDuration(customer.rate_limit.token_reset_duration || "1h")}
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
																			{customer.rate_limit.token_current_usage.toLocaleString()} /{" "}
																			{customer.rate_limit.token_max_limit.toLocaleString()} tokens
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			Resets {formatResetDuration(customer.rate_limit.token_reset_duration || "1h")}
																		</p>
																	</TooltipContent>
																</Tooltip>
															)}
															{customer.rate_limit.request_max_limit && (
																<Tooltip>
																	<TooltipTrigger asChild>
																		<div className="space-y-1.5">
																			<div className="flex items-center justify-between gap-4 text-xs">
																				<span className="font-medium">{customer.rate_limit.request_max_limit.toLocaleString()} req</span>
																				<span className="text-muted-foreground">
																					{formatResetDuration(customer.rate_limit.request_reset_duration || "1h")}
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
																			{customer.rate_limit.request_current_usage.toLocaleString()} /{" "}
																			{customer.rate_limit.request_max_limit.toLocaleString()} requests
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			Resets {formatResetDuration(customer.rate_limit.request_reset_duration || "1h")}
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
													{vks?.length > 0 ? (
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
													className={cn(
														"dark:bg-card dark:group-hover:bg-muted",
														isExhausted ? "bg-red-500/5 group-hover:bg-red-500/10" : "bg-white group-hover:bg-muted",
														ACTIONS_COLUMN_CLASS,
													)}
												>
													<CustomerActionsMenu
														customer={customer}
														canUpdate={hasUpdateAccess}
														canDelete={hasDeleteAccess}
														onEdit={handleEditCustomer}
														onDelete={setConfirmDeleteCustomer}
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
									data-testid="customers-pagination-prev-btn"
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
									data-testid="customers-pagination-next-btn"
									aria-label="Next page"
								>
									<ChevronRight className="size-3" />
								</Button>
							</div>
						</div>
					)}
				</div>

				<AlertDialog open={!!confirmDeleteCustomer} onOpenChange={(open) => !open && setConfirmDeleteCustomer(null)}>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete Customer</AlertDialogTitle>
							<AlertDialogDescription>
								Are you sure you want to delete &quot;{confirmDeleteCustomer?.name}&quot;? This will also delete all associated teams and
								unassign any virtual keys. This action cannot be undone.
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter>
							<AlertDialogCancel data-testid="customer-button-delete-cancel">Cancel</AlertDialogCancel>
							<AlertDialogAction
								data-testid="customer-button-delete-confirm"
								onClick={() => confirmDeleteCustomer && handleDelete(confirmDeleteCustomer.id)}
								disabled={isDeleting}
								className="bg-red-600 hover:bg-red-700"
							>
								{isDeleting ? "Deleting..." : "Delete"}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			</TooltipProvider>
		</>
	);
}