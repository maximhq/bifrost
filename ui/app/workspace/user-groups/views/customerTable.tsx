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
import { resetDurationLabels } from "@/lib/constants/governance";
import { getErrorMessage, useDeleteCustomerMutation } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Edit, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import CustomerDialog from "./customerDialog";
import { CustomersEmptyState } from "./customersEmptyState";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

interface CustomersTableProps {
	customers: Customer[];
	teams: Team[];
	virtualKeys: VirtualKey[];
}

export default function CustomersTable({ customers, teams, virtualKeys }: CustomersTableProps) {
	const [showCustomerDialog, setShowCustomerDialog] = useState(false);
	const [editingCustomer, setEditingCustomer] = useState<Customer | null>(null);

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
		}
	};

	const handleAddCustomer = () => {
		setEditingCustomer(null);
		setShowCustomerDialog(true);
	};

	const handleEditCustomer = (customer: Customer) => {
		setEditingCustomer(customer);
		setShowCustomerDialog(true);
	};

	const handleCustomerSaved = () => {
		setShowCustomerDialog(false);
		setEditingCustomer(null);
	};

	const getTeamsForCustomer = (customerId: string) => {
		return teams.filter((team) => team.customer_id === customerId);
	};

	const getVirtualKeysForCustomer = (customerId: string) => {
		return virtualKeys.filter((vk) => vk.customer_id === customerId);
	};

	// Empty state when user has no customers (same pattern as Virtual Keys)
	if (customers?.length === 0) {
		return (
			<>
				<TooltipProvider>
					{showCustomerDialog && (
						<CustomerDialog customer={editingCustomer} onSave={handleCustomerSaved} onCancel={() => setShowCustomerDialog(false)} />
					)}
					<CustomersEmptyState onAddClick={handleAddCustomer} canCreate={hasCreateAccess} />
				</TooltipProvider>
			</>
		);
	}

	return (
		<>
			<TooltipProvider>
				{showCustomerDialog && (
					<CustomerDialog customer={editingCustomer} onSave={handleCustomerSaved} onCancel={() => setShowCustomerDialog(false)} />
				)}

				<div className="space-y-4">
					<div className="flex items-center justify-between">
						<div>
							<h2 className="text-lg font-semibold">Customers</h2>
							<p className="text-muted-foreground text-sm">Manage customer accounts with their own teams, budgets, and access controls.</p>
						</div>
						<Button data-testid="create-customer-btn" onClick={handleAddCustomer} disabled={!hasCreateAccess}>
							<Plus className="h-4 w-4" />
							Add Customer
						</Button>
					</div>

					<div className="rounded-sm border" data-testid="customers-table">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Teams</TableHead>
									<TableHead>Budget</TableHead>
									<TableHead>Rate Limit</TableHead>
									<TableHead>Virtual Keys</TableHead>
									<TableHead className="text-right"></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{customers?.map((customer) => {
										const customerTeams = getTeamsForCustomer(customer.id);
										const vks = getVirtualKeysForCustomer(customer.id);

										// Budget calculations
										const isBudgetExhausted =
											customer.budget?.max_limit && customer.budget.max_limit > 0 && customer.budget.current_usage >= customer.budget.max_limit;
										const budgetPercentage =
											customer.budget?.max_limit && customer.budget.max_limit > 0
												? Math.min((customer.budget.current_usage / customer.budget.max_limit) * 100, 100)
												: 0;

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
											<TableRow key={customer.id} className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}>
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
													{customer.budget ? (
														<Tooltip>
															<TooltipTrigger asChild>
																<div className="space-y-2">
																	<div className="flex items-center justify-between gap-4">
																		<span className="font-medium">{formatCurrency(customer.budget.max_limit)}</span>
																		<span className="text-muted-foreground text-xs">
																			{formatResetDuration(customer.budget.reset_duration)}
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
																	{formatCurrency(customer.budget.current_usage)} / {formatCurrency(customer.budget.max_limit)}
																</p>
																<p className="text-primary-foreground/80 text-xs">
																	Resets {formatResetDuration(customer.budget.reset_duration)}
																</p>
															</TooltipContent>
														</Tooltip>
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
												<TableCell className="text-right">
													<div className="flex items-center justify-end gap-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
														<Button
															variant="ghost"
															size="icon"
															className="h-8 w-8"
															onClick={() => handleEditCustomer(customer)}
															disabled={!hasUpdateAccess}
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
																>
																	<Trash2 className="h-4 w-4" />
																</Button>
															</AlertDialogTrigger>
															<AlertDialogContent>
																<AlertDialogHeader>
																	<AlertDialogTitle>Delete Customer</AlertDialogTitle>
																	<AlertDialogDescription>
																		Are you sure you want to delete &quot;{customer.name}&quot;? This will also delete all associated teams
																		and unassign any virtual keys. This action cannot be undone.
																	</AlertDialogDescription>
																</AlertDialogHeader>
																<AlertDialogFooter>
																	<AlertDialogCancel>Cancel</AlertDialogCancel>
																	<AlertDialogAction
																		onClick={() => handleDelete(customer.id)}
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
				</div>
			</TooltipProvider>
		</>
	);
}
