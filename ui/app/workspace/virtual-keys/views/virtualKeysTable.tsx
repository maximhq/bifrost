import { RateLimitDisplay } from "@/components/rateLimitDisplay";
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
import { ComboboxSelect } from "@/components/ui/combobox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { resetDurationLabels } from "@/lib/constants/governance";
import { getErrorMessage, useDeleteVirtualKeyMutation, useLazyGetVirtualKeysQuery } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import {
	ArrowDown,
	ArrowUp,
	ArrowUpDown,
	ChevronLeft,
	ChevronRight,
	Copy,
	Download,
	Edit,
	Eye,
	EyeOff,
	Loader2,
	Plus,
	Search,
	ShieldCheck,
	Trash2,
} from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { useVirtualKeyUsage } from "../hooks/useVirtualKeyUsage";
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet";
import { VirtualKeysEmptyState } from "./virtualKeysEmptyState";
import VirtualKeySheet from "./virtualKeySheet";

const formatResetDuration = (duration: string) => resetDurationLabels[duration] || duration;

type ExportScope = "current_page" | "all";

function virtualKeysToCSV(vks: VirtualKey[]): string {
	const headers = ["Name", "Status", "Assigned To", "Budget Limit", "Budget Spent", "Budget Reset", "Description", "Created At"];
	const rows = vks.map((vk) => {
		const isExhausted =
			vk.budgets?.some((b) => b.current_usage >= b.max_limit) ||
			(vk.rate_limit?.token_current_usage &&
				vk.rate_limit?.token_max_limit &&
				vk.rate_limit.token_current_usage >= vk.rate_limit.token_max_limit) ||
			(vk.rate_limit?.request_current_usage &&
				vk.rate_limit?.request_max_limit &&
				vk.rate_limit.request_current_usage >= vk.rate_limit.request_max_limit);
		const status = vk.is_active ? (isExhausted ? "Exhausted" : "Active") : "Inactive";
		const assignedTo = vk.team ? `Team: ${vk.team.name}` : vk.customer ? `Customer: ${vk.customer.name}` : "";
		const budgetLimit = vk.budgets?.length ? vk.budgets.map((b) => formatCurrency(b.max_limit)).join("; ") : "";
		const budgetSpent = vk.budgets?.length ? vk.budgets.map((b) => formatCurrency(b.current_usage)).join("; ") : "";
		const budgetReset = vk.budgets?.length ? vk.budgets.map((b) => formatResetDuration(b.reset_duration)).join("; ") : "";
		return [vk.name, status, assignedTo, budgetLimit, budgetSpent, budgetReset, vk.description || "", vk.created_at];
	});
	return [headers, ...rows].map((row) => row.map((cell) => `"${String(cell).replace(/"/g, '""')}"`).join(",")).join("\n");
}

function downloadCSV(content: string) {
	const blob = new Blob([content], { type: "text/csv;charset=utf-8;" });
	const url = URL.createObjectURL(blob);
	const link = document.createElement("a");
	link.href = url;
	link.download = `virtual-keys-${new Date().toISOString().split("T")[0]}.csv`;
	link.click();
	URL.revokeObjectURL(url);
}

function VKBudgetCell({ vk }: { vk: VirtualKey }) {
	const { displayBudgets } = useVirtualKeyUsage(vk);

	if (!displayBudgets || displayBudgets.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}

	return (
		<div className="flex flex-col gap-0.5">
			{displayBudgets.map((b, idx) => (
				<div key={idx} className="flex flex-col">
					<span className={cn("font-mono text-sm", b.current_usage >= b.max_limit && "text-red-400")}>
						{formatCurrency(b.current_usage)} / {formatCurrency(b.max_limit)}
					</span>
					<span className="text-muted-foreground text-xs">
						Resets {formatResetDuration(b.reset_duration)}
						{vk.calendar_aligned && " (calendar)"}
					</span>
				</div>
			))}
		</div>
	);
}

function VKRateLimitCell({ vk }: { vk: VirtualKey }) {
	const { displayRateLimit } = useVirtualKeyUsage(vk);
	return <RateLimitDisplay rateLimits={displayRateLimit} />;
}

// Status badge derives exhaustion from the same AP-backed source as the budget/rate-limit cells
// so managed keys don't show "Active" next to an exhausted-looking bar.
function VKStatusBadge({ vk }: { vk: VirtualKey }) {
	const { isExhausted } = useVirtualKeyUsage(vk);
	return (
		<Badge variant={vk.is_active ? (isExhausted ? "destructive" : "default") : "secondary"}>
			{vk.is_active ? (isExhausted ? "Exhausted" : "Active") : "Inactive"}
		</Badge>
	);
}

// Per-row delete button. Calls useVirtualKeyUsage (same cached query as the budget/
// rate-limit cells — RTK dedupes) to detect managed-by-AP VKs and swap the normal
// delete AlertDialog for a disabled button + tooltip so users aren't lured into a
// confirm-then-403 loop.
function VKDeleteButton({
	vk,
	hasDeleteAccess,
	isDeleting,
	onDelete,
}: {
	vk: VirtualKey;
	hasDeleteAccess: boolean;
	isDeleting: boolean;
	onDelete: (vkId: string) => void;
}) {
	const { isManagedByProfile } = useVirtualKeyUsage(vk);

	if (isManagedByProfile) {
		return (
			<TooltipProvider>
				<Tooltip delayDuration={300}>
					<TooltipTrigger asChild>
						<span className="inline-block cursor-not-allowed">
							<Button
								variant="ghost"
								size="sm"
								className="text-destructive border-destructive/30"
								disabled
								data-testid={`vk-delete-btn-${vk.name}`}
							>
								<Trash2 className="h-4 w-4" />
							</Button>
						</span>
					</TooltipTrigger>
					<TooltipContent side="top" className="max-w-[260px]">
						<p className="text-xs">
							This virtual key is managed by an access profile and can&apos;t be deleted here. Detach the profile from the user or delete it
							from the access profile settings.
						</p>
					</TooltipContent>
				</Tooltip>
			</TooltipProvider>
		);
	}

	return (
		<AlertDialog>
			<AlertDialogTrigger asChild>
				<Button
					variant="ghost"
					size="sm"
					className="text-destructive hover:bg-destructive/10 hover:text-destructive border-destructive/30"
					onClick={(e) => e.stopPropagation()}
					disabled={!hasDeleteAccess}
					data-testid={`vk-delete-btn-${vk.name}`}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</AlertDialogTrigger>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete Virtual Key</AlertDialogTitle>
					<AlertDialogDescription>
						Are you sure you want to delete &quot;{vk.name.length > 20 ? `${vk.name.slice(0, 20)}...` : vk.name}&quot;? This action cannot be undone.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel data-testid={`vk-delete-cancel-${vk.name}`}>Cancel</AlertDialogCancel>
					<AlertDialogAction
						onClick={() => onDelete(vk.id)}
						disabled={isDeleting}
						className="bg-destructive hover:bg-destructive/90"
						data-testid={`vk-delete-confirm-${vk.name}`}
					>
						{isDeleting ? "Deleting..." : "Delete"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

interface VirtualKeysTableProps {
	virtualKeys: VirtualKey[];
	totalCount: number;
	teams: Team[];
	customers: Customer[];
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	customerFilter: string;
	onCustomerFilterChange: (value: string) => void;
	teamFilter: string;
	onTeamFilterChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
	sortBy?: string;
	order?: string;
	onSortChange: (sortBy: string, order: string) => void;
}

export default function VirtualKeysTable({
	virtualKeys,
	totalCount,
	teams,
	customers,
	search,
	debouncedSearch,
	onSearchChange,
	customerFilter,
	onCustomerFilterChange,
	teamFilter,
	onTeamFilterChange,
	offset,
	limit,
	onOffsetChange,
	sortBy,
	order,
	onSortChange,
}: VirtualKeysTableProps) {
	const [showVirtualKeySheet, setShowVirtualKeySheet] = useState(false);
	const [editingVirtualKeyId, setEditingVirtualKeyId] = useState<string | null>(null);
	const [revealedKeys, setRevealedKeys] = useState<Set<string>>(new Set());
	const [selectedVirtualKeyId, setSelectedVirtualKeyId] = useState<string | null>(null);
	const [showDetailSheet, setShowDetailSheet] = useState(false);
	const [showExportDialog, setShowExportDialog] = useState(false);
	const [exportScope, setExportScope] = useState<ExportScope>("current_page");
	const [exportMaxLimit, setExportMaxLimit] = useState("");
	const [fetchVirtualKeys, { isFetching: isExporting }] = useLazyGetVirtualKeysQuery();

	// Derive objects from props so they stay in sync with RTK cache updates
	const editingVirtualKey = useMemo(
		() => (editingVirtualKeyId ? (virtualKeys.find((vk) => vk.id === editingVirtualKeyId) ?? null) : null),
		[editingVirtualKeyId, virtualKeys],
	);
	const selectedVirtualKey = useMemo(
		() => (selectedVirtualKeyId ? (virtualKeys.find((vk) => vk.id === selectedVirtualKeyId) ?? null) : null),
		[selectedVirtualKeyId, virtualKeys],
	);

	const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Delete);

	const [deleteVirtualKey, { isLoading: isDeleting }] = useDeleteVirtualKeyMutation();

	const handleDelete = async (vkId: string) => {
		try {
			await deleteVirtualKey(vkId).unwrap();
			toast.success("Virtual key deleted successfully");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddVirtualKey = () => {
		setEditingVirtualKeyId(null);
		setShowVirtualKeySheet(true);
	};

	const handleEditVirtualKey = (vk: VirtualKey, e: React.MouseEvent) => {
		e.stopPropagation(); // Prevent row click
		setEditingVirtualKeyId(vk.id);
		setShowVirtualKeySheet(true);
	};

	const handleVirtualKeySaved = () => {
		setShowVirtualKeySheet(false);
		setEditingVirtualKeyId(null);
	};

	const handleRowClick = (vk: VirtualKey) => {
		setSelectedVirtualKeyId(vk.id);
		setShowDetailSheet(true);
	};

	const handleDetailSheetClose = () => {
		setShowDetailSheet(false);
		setSelectedVirtualKeyId(null);
	};

	const toggleKeyVisibility = (vkId: string) => {
		const newRevealed = new Set(revealedKeys);
		if (newRevealed.has(vkId)) {
			newRevealed.delete(vkId);
		} else {
			newRevealed.add(vkId);
		}
		setRevealedKeys(newRevealed);
	};

	const maskKey = (key: string, revealed: boolean) => {
		if (revealed) return key;
		return key.substring(0, 8) + "•".repeat(Math.max(0, key.length - 8));
	};

	const { copy: copyToClipboard } = useCopyToClipboard();

	const hasActiveFilters = debouncedSearch || customerFilter || teamFilter;

	const toggleSort = (column: string) => {
		if (sortBy === column) {
			if (order === "asc") {
				onSortChange(column, "desc");
			} else {
				// Clicking again clears sort
				onSortChange("", "");
			}
		} else {
			onSortChange(column, "asc");
		}
	};

	const handleExportCSV = async () => {
		if (exportScope === "current_page") {
			downloadCSV(virtualKeysToCSV(virtualKeys));
			toast.success(`Exported ${virtualKeys.length} virtual keys`);
			setShowExportDialog(false);
			return;
		}

		// Fetch all with same filters/sort applied
		const maxLimit = exportMaxLimit ? parseInt(exportMaxLimit, 10) : undefined;
		const fetchLimit = maxLimit && maxLimit > 0 ? maxLimit : 10000;

		try {
			const result = await fetchVirtualKeys({
				limit: fetchLimit,
				offset: 0,
				search: debouncedSearch || undefined,
				customer_id: customerFilter || undefined,
				team_id: teamFilter || undefined,
				sort_by: (sortBy as "name" | "budget_spent" | "created_at" | "status") || undefined,
				order: (order as "asc" | "desc") || undefined,
				export: true,
			}).unwrap();

			downloadCSV(virtualKeysToCSV(result.virtual_keys));
			toast.success(`Exported ${result.virtual_keys.length} virtual keys`);
			setShowExportDialog(false);
		} catch (error) {
			toast.error(`Export failed: ${getErrorMessage(error)}`);
		}
	};

	const openExportDialog = () => {
		setExportScope("current_page");
		setExportMaxLimit("");
		setShowExportDialog(true);
	};

	const SortableHeader = ({ column, label }: { column: string; label: string }) => {
		const isActive = sortBy === column;
		const Icon = isActive ? (order === "desc" ? ArrowDown : ArrowUp) : ArrowUpDown;
		return (
			<Button variant="ghost" onClick={() => toggleSort(column)} data-testid={`vk-sort-${column}`} className="!px-0">
				{label}
				<Icon className={cn("ml-2 h-4 w-4", isActive && "text-foreground")} />
			</Button>
		);
	};


	// True empty state: no VKs at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				{showVirtualKeySheet && (
					<VirtualKeySheet
						virtualKey={editingVirtualKey}
						teams={teams}
						customers={customers}
						onSave={handleVirtualKeySaved}
						onCancel={() => setShowVirtualKeySheet(false)}
					/>
				)}
				<VirtualKeysEmptyState onAddClick={handleAddVirtualKey} canCreate={hasCreateAccess} />
			</>
		);
	}

	return (
		<>
			{showVirtualKeySheet && (
				<VirtualKeySheet
					virtualKey={editingVirtualKey}
					teams={teams}
					customers={customers}
					onSave={handleVirtualKeySaved}
					onCancel={() => setShowVirtualKeySheet(false)}
				/>
			)}

			{showDetailSheet && selectedVirtualKey && <VirtualKeyDetailSheet virtualKey={selectedVirtualKey} onClose={handleDetailSheetClose} />}

			{/* Export Dialog */}
			<Dialog open={showExportDialog} onOpenChange={setShowExportDialog}>
				<DialogContent className="sm:max-w-[425px]">
					<DialogHeader className="pb-0">
						<DialogTitle>Export Virtual Keys</DialogTitle>
						<DialogDescription>Download as CSV with current filters and sorting applied.</DialogDescription>
					</DialogHeader>
					<div className="space-y-4">
						<div className="space-y-2">
							<Label className="text-sm">Export scope</Label>
							<div className="grid grid-cols-2 gap-2" data-testid="vk-export-scope">
								<button
									type="button"
									onClick={() => setExportScope("current_page")}
									className={cn(
										"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
										exportScope === "current_page"
											? "border-primary bg-primary/5 text-foreground"
											: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									)}
								>
									<span className="font-medium">Current page</span>
									<span className="text-muted-foreground text-xs">{virtualKeys.length} entries</span>
								</button>
								<button
									type="button"
									onClick={() => setExportScope("all")}
									className={cn(
										"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
										exportScope === "all"
											? "border-primary bg-primary/5 text-foreground"
											: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									)}
								>
									<span className="font-medium">All entries</span>
									<span className="text-muted-foreground text-xs">{totalCount} total</span>
								</button>
							</div>
						</div>

						{exportScope === "all" && (
							<div className="space-y-2">
								<Label htmlFor="export-max-limit" className="text-sm">
									Max entries <span className="text-muted-foreground font-normal">(optional)</span>
								</Label>
								<Input
									id="export-max-limit"
									type="number"
									min="1"
									placeholder={`Leave blank for all ${totalCount}`}
									value={exportMaxLimit}
									onChange={(e) => setExportMaxLimit(e.target.value)}
									data-testid="vk-export-max-limit"
								/>
							</div>
						)}

						{hasActiveFilters && (
							<p className="text-muted-foreground text-xs">
								Filters applied:{" "}
								{[debouncedSearch && `search "${debouncedSearch}"`, customerFilter && "customer filter", teamFilter && "team filter"]
									.filter(Boolean)
									.join(", ")}
							</p>
						)}

						<div className="text-muted-foreground flex items-center gap-2">
							<ShieldCheck className="h-3.5 w-3.5 shrink-0" />
							<p className="text-xs">API tokens are excluded from the export.</p>
						</div>
					</div>
					<DialogFooter className="pt-0">
						<Button variant="outline" onClick={() => setShowExportDialog(false)} disabled={isExporting}>
							Cancel
						</Button>
						<Button onClick={handleExportCSV} disabled={isExporting} data-testid="vk-export-confirm-btn">
							{isExporting ? (
								<>
									<Loader2 className="h-4 w-4 animate-spin" />
									Exporting...
								</>
							) : (
								<>
									<Download className="h-4 w-4" />
									Export CSV
								</>
							)}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>

			<div className="space-y-4">
				<div className="flex items-center justify-between">
					<div>
						<h2 className="text-lg font-semibold">Virtual Keys</h2>
						<p className="text-muted-foreground text-sm">Manage virtual keys, their permissions, budgets, and rate limits.</p>
					</div>
					<div className="flex items-center gap-2">
						<Button variant="outline" onClick={openExportDialog} disabled={virtualKeys.length === 0} data-testid="vk-export-btn">
							<Download className="h-4 w-4" />
							Export CSV
						</Button>
						<Button onClick={handleAddVirtualKey} disabled={!hasCreateAccess} data-testid="create-vk-btn">
							<Plus className="h-4 w-4" />
							Add Virtual Key
						</Button>
					</div>
				</div>

				{/* Toolbar: Search + Filters */}
				<div className="flex items-center gap-3">
					<div className="relative max-w-sm flex-1">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label="Search virtual keys by name"
							placeholder="Search by name..."
							value={search}
							onChange={(e) => onSearchChange(e.target.value)}
							className="pl-9"
							data-testid="vk-search-input"
						/>
					</div>
					<ComboboxSelect
						data-testid="vk-customer-filter"
						options={customers.map((c) => ({ label: c.name, value: c.id }))}
						value={customerFilter || null}
						onValueChange={(val) => onCustomerFilterChange(val ?? "")}
						placeholder="All Customers"
						className="w-[180px] h-9"
					/>
					{customerFilter && teamFilter && <span className="text-muted-foreground text-xs font-medium">or</span>}
					<ComboboxSelect
						data-testid="vk-team-filter"
						options={teams.map((t) => ({ label: t.name, value: t.id }))}
						value={teamFilter || null}
						onValueChange={(val) => onTeamFilterChange(val ?? "")}
						placeholder="All Teams"
						className="w-[180px] h-9"
					/>
				</div>

				<div className="rounded-sm border">
					<Table className="table-fixed w-full" data-testid="vk-table">
						<TableHeader>
							<TableRow>
								<TableHead className="w-[250px]">
									<SortableHeader column="name" label="Name" />
								</TableHead>
								<TableHead className="w-[160px]">Assigned To</TableHead>
								<TableHead className="w-[440px]">Key</TableHead>
								<TableHead className="w-[200px]">
									<SortableHeader column="budget_spent" label="Budget" />
								</TableHead>
								<TableHead className="w-[200px]">Rate Limits</TableHead>
								<TableHead className="w-[120px]">
									<SortableHeader column="status" label="Status" />
								</TableHead>
								<TableHead className="w-[110px] text-right"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{virtualKeys.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching virtual keys found.</span>
									</TableCell>
								</TableRow>
							) : (
								virtualKeys.map((vk) => {
									const isRevealed = revealedKeys.has(vk.id);

									return (
										<TableRow
											key={vk.id}
											data-testid={`vk-row-${vk.name}`}
											className="hover:bg-muted/50 cursor-pointer transition-colors"
											onClick={() => handleRowClick(vk)}
										>
											<TableCell className="max-w-[200px]">
												<div className="truncate font-medium">{vk.name}</div>
											</TableCell>
											<TableCell>
												{vk.team ? (
													<Badge variant="outline" className="max-w-full truncate text-left block">Team: {vk.team.name}</Badge>
												) : vk.customer ? (
													<Badge variant="outline" className="max-w-full truncate text-left block">Customer: {vk.customer.name}</Badge>
												) : (
													<span className="text-muted-foreground text-sm truncate max-w-full text-left">-</span>
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center gap-2">
													<code className="cursor-default py-1 font-mono text-sm" data-testid="vk-key-value">
														{maskKey(vk.value, isRevealed)}
													</code>
													<div className="flex items-center">
														<Button
															variant="ghost"
															size="sm"
															onClick={() => toggleKeyVisibility(vk.id)}
															data-testid={`vk-visibility-btn-${vk.name}`}
														>
															{isRevealed ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
														</Button>
														<Button
															variant="ghost"
															size="sm"
															onClick={() => copyToClipboard(vk.value)}
															data-testid={`vk-copy-btn-${vk.name}`}
														>
															<Copy className="h-4 w-4" />
														</Button>
													</div>
												</div>
											</TableCell>
											<TableCell>
												<VKBudgetCell vk={vk} />
											</TableCell>
											<TableCell>
												<VKRateLimitCell vk={vk} />
											</TableCell>
											<TableCell>
												<VKStatusBadge vk={vk} />
											</TableCell>
											<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center justify-end gap-2">
													<Button
														variant="ghost"
														size="sm"
														onClick={(e) => handleEditVirtualKey(vk, e)}
														disabled={!hasUpdateAccess}
														data-testid={`vk-edit-btn-${vk.name}`}
													>
														<Edit className="h-4 w-4" />
													</Button>
													<VKDeleteButton vk={vk} hasDeleteAccess={hasDeleteAccess} isDeleting={isDeleting} onDelete={handleDelete} />
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
								data-testid="vk-pagination-prev-btn"
							>
								<ChevronLeft className="mr-1 h-4 w-4" />
								Previous
							</Button>
							<Button
								variant="outline"
								size="sm"
								disabled={offset + limit >= totalCount}
								onClick={() => onOffsetChange(offset + limit)}
								data-testid="vk-pagination-next-btn"
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