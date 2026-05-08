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
import { Input } from "@/components/ui/input";
import { ChevronLeft, ChevronRight, Edit, Plus, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import CustomerDialog from "./customerDialog";
import { CustomersEmptyState } from "./customersEmptyState";

// Helper to format reset duration for display
const formatResetDuration = (duration: string) => {
	return resetDurationLabels[duration] || duration;
};

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
}: CustomersTableProps) {
	const { t } = useTranslation();
	const [showCustomerDialog, setShowCustomerDialog] = useState(false);
	const [editingCustomer, setEditingCustomer] = useState<Customer | null>(null);

	const hasCreateAccess = useRbac(RbacResource.Customers, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Customers, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Customers, RbacOperation.Delete);

	const [deleteCustomer, { isLoading: isDeleting }] = useDeleteCustomerMutation();

	const handleDelete = async (customerId: string) => {
		try {
			await deleteCustomer(customerId).unwrap();
			toast.success(t("workspace.governance.customers.deleteSuccess"));
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

	const hasActiveFilters = debouncedSearch;

	// True empty state: no customers at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
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
							<h2 className="text-lg font-semibold">{t("workspace.governance.customers.title")}</h2>
							<p className="text-muted-foreground text-sm">{t("workspace.governance.customers.description")}</p>
						</div>
						<Button data-testid="customer-button-create" onClick={handleAddCustomer} disabled={!hasCreateAccess}>
							<Plus className="h-4 w-4" />
							{t("workspace.governance.customers.addCustomer")}
						</Button>
					</div>

					<div className="flex items-center gap-3">
						<div className="relative max-w-sm flex-1">
							<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
							<Input
								aria-label={t("workspace.governance.customers.searchAriaLabel")}
								placeholder={t("workspace.governance.customers.searchPlaceholder")}
								value={search}
								onChange={(e) => onSearchChange(e.target.value)}
								className="pl-9"
								data-testid="customers-search-input"
							/>
						</div>
					</div>

					<div className="overflow-hidden rounded-sm border" data-testid="customer-table-container">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>{t("workspace.governance.customers.name")}</TableHead>
									<TableHead>{t("workspace.governance.customers.teams")}</TableHead>
									<TableHead>{t("workspace.governance.customers.budget")}</TableHead>
									<TableHead>{t("workspace.governance.customers.rateLimit")}</TableHead>
									<TableHead>{t("workspace.governance.customers.virtualKeys")}</TableHead>
									<TableHead className="text-right"></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{customers.length === 0 ? (
									<TableRow>
										<TableCell colSpan={6} className="h-24 text-center">
											<span className="text-muted-foreground text-sm">{t("workspace.governance.customers.noMatching")}</span>
										</TableCell>
									</TableRow>
								) : (
									customers.map((customer) => {
										const customerTeams = getTeamsForCustomer(customer.id);
										const vks = getVirtualKeysForCustomer(customer.id);

										// Budget calculations
										const isBudgetExhausted =
											customer.budget?.max_limit &&
											customer.budget.max_limit > 0 &&
											customer.budget.current_usage >= customer.budget.max_limit;
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
											<TableRow
												key={customer.id}
												data-testid={`customer-row-${customer.name}`}
												className={cn("group transition-colors", isExhausted && "bg-red-500/5 hover:bg-red-500/10")}
											>
												<TableCell className="max-w-[200px] py-4">
													<div className="flex flex-col gap-2">
														<span className="truncate font-medium">{customer.name}</span>
														{isExhausted && (
															<Badge variant="destructive" className="w-fit text-xs">
																{t("workspace.governance.customers.limitReached")}
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
																		{t("workspace.governance.customers.teamCount", { count: customerTeams.length })}
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
																	{t("workspace.governance.customers.resets", {
																		duration: formatResetDuration(customer.budget.reset_duration),
																	})}
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
																				<span className="font-medium">
																					{customer.rate_limit.token_max_limit.toLocaleString()}{" "}
																					{t("workspace.governance.customers.tokensUnit")}
																				</span>
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
																			{customer.rate_limit.token_max_limit.toLocaleString()}{" "}
																			{t("workspace.governance.customers.tokensUnit")}
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			{t("workspace.governance.customers.resets", {
																				duration: formatResetDuration(customer.rate_limit.token_reset_duration || "1h"),
																			})}
																		</p>
																	</TooltipContent>
																</Tooltip>
															)}
															{customer.rate_limit.request_max_limit && (
																<Tooltip>
																	<TooltipTrigger asChild>
																		<div className="space-y-1.5">
																			<div className="flex items-center justify-between gap-4 text-xs">
																				<span className="font-medium">
																					{customer.rate_limit.request_max_limit.toLocaleString()}{" "}
																					{t("workspace.governance.customers.requestUnitShort")}
																				</span>
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
																			{customer.rate_limit.request_max_limit.toLocaleString()}{" "}
																			{t("workspace.governance.customers.requestsUnit")}
																		</p>
																		<p className="text-primary-foreground/80 text-xs">
																			{t("workspace.governance.customers.resets", {
																				duration: formatResetDuration(customer.rate_limit.request_reset_duration || "1h"),
																			})}
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
																		{t("workspace.governance.customers.keyCount", { count: vks.length })}
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
															onClick={() => handleEditCustomer(customer)}
															disabled={!hasUpdateAccess}
															aria-label={t("workspace.governance.customers.editAriaLabel", { name: customer.name })}
															data-testid={`customer-button-edit-${customer.id}`}
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
																	aria-label={t("workspace.governance.customers.deleteAriaLabel", { name: customer.name })}
																	data-testid={`customer-button-delete-${customer.id}`}
																>
																	<Trash2 className="h-4 w-4" />
																</Button>
															</AlertDialogTrigger>
															<AlertDialogContent>
																<AlertDialogHeader>
																	<AlertDialogTitle>{t("workspace.governance.customers.deleteTitle")}</AlertDialogTitle>
																	<AlertDialogDescription>
																		{t("workspace.governance.customers.deleteDescription", { name: customer.name })}
																	</AlertDialogDescription>
																</AlertDialogHeader>
																<AlertDialogFooter>
																	<AlertDialogCancel data-testid="customer-button-delete-cancel">{t("common.cancel")}</AlertDialogCancel>
																	<AlertDialogAction
																		data-testid="customer-button-delete-confirm"
																		onClick={() => handleDelete(customer.id)}
																		disabled={isDeleting}
																		className="bg-red-600 hover:bg-red-700"
																	>
																		{isDeleting ? t("workspace.providers.deleteDialog.deleting") : t("common.delete")}
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
								{t("workspace.governance.customers.showing", {
									from: offset + 1,
									to: Math.min(offset + limit, totalCount),
									total: totalCount,
								})}
							</p>
							<div className="flex gap-2">
								<Button
									variant="outline"
									size="sm"
									disabled={offset === 0}
									onClick={() => onOffsetChange(Math.max(0, offset - limit))}
									data-testid="customers-pagination-prev-btn"
								>
									<ChevronLeft className="mr-1 h-4 w-4" /> {t("workspace.governance.customers.previous")}
								</Button>
								<Button
									variant="outline"
									size="sm"
									disabled={offset + limit >= totalCount}
									onClick={() => onOffsetChange(offset + limit)}
									data-testid="customers-pagination-next-btn"
								>
									{t("workspace.governance.customers.next")} <ChevronRight className="ml-1 h-4 w-4" />
								</Button>
							</div>
						</div>
					)}
				</div>
			</TooltipProvider>
		</>
	);
}