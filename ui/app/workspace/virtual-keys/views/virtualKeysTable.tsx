"use client"

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
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { getErrorMessage, useDeleteVirtualKeyMutation, useBulkDeleteVirtualKeysMutation } from "@/lib/store"
import { Customer, Team, VirtualKey } from "@/lib/types/governance"
import { resetDurationLabels } from "@/lib/constants/governance"
import { cn } from "@/lib/utils"
import { formatCurrency } from "@/lib/utils/governance"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard"
import { ChevronLeft, ChevronRight, Copy, Edit, Eye, EyeOff, Plus, Search, Trash2 } from "lucide-react"
import { useMemo, useState } from "react"
import { toast } from "sonner"
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet"
import { VirtualKeysEmptyState } from "./virtualKeysEmptyState"
import VirtualKeySheet from "./virtualKeySheet"

const formatResetDuration = (duration: string) => resetDurationLabels[duration] || duration

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
}: VirtualKeysTableProps) {
  const [showVirtualKeySheet, setShowVirtualKeySheet] = useState(false)
  const [editingVirtualKeyId, setEditingVirtualKeyId] = useState<string | null>(null)
  const [revealedKeys, setRevealedKeys] = useState<Set<string>>(new Set())
  const [selectedVirtualKeyId, setSelectedVirtualKeyId] = useState<string | null>(null)
  const [showDetailSheet, setShowDetailSheet] = useState(false)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [showBulkDeleteDialog, setShowBulkDeleteDialog] = useState(false)
  const [isBulkDeleting, setIsBulkDeleting] = useState(false)

  const editingVirtualKey = useMemo(
    () => (editingVirtualKeyId ? virtualKeys.find((vk) => vk.id === editingVirtualKeyId) ?? null : null),
    [editingVirtualKeyId, virtualKeys],
  )
  const selectedVirtualKey = useMemo(
    () => (selectedVirtualKeyId ? virtualKeys.find((vk) => vk.id === selectedVirtualKeyId) ?? null : null),
    [selectedVirtualKeyId, virtualKeys],
  )

  const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create)
  const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update)
  const hasDeleteAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Delete)

  const [deleteVirtualKey, { isLoading: isDeleting }] = useDeleteVirtualKeyMutation()
  const [bulkDeleteVirtualKeys] = useBulkDeleteVirtualKeysMutation()

	const handleDelete = async (vkId: string) => {
		try {
			await deleteVirtualKey(vkId).unwrap();
			toast.success("Virtual key deleted successfully");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const toggleRowSelection = (id: string) => {
		const newSelected = new Set(selectedIds);
		if (newSelected.has(id)) {
			newSelected.delete(id);
		} else {
			newSelected.add(id);
		}
		setSelectedIds(newSelected);
	};

	const toggleSelectAll = () => {
		if (selectedIds.size === virtualKeys.length && virtualKeys.length > 0) {
			setSelectedIds(new Set());
		} else {
			setSelectedIds(new Set(virtualKeys.map(vk => vk.id)));
		}
	};

	const handleBulkDelete = async () => {
		setIsBulkDeleting(true);
		try {
			const keysToDelete = Array.from(selectedIds);
			await bulkDeleteVirtualKeys(keysToDelete).unwrap();
			toast.success(`${keysToDelete.length} virtual key(s) deleted successfully`);
			setSelectedIds(new Set());
			setShowBulkDeleteDialog(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		} finally {
			setIsBulkDeleting(false);
		}
	};

	const handleAddVirtualKey = () => {
		setEditingVirtualKeyId(null);
		setShowVirtualKeySheet(true);
	};

	const handleEditVirtualKey = (vk: VirtualKey, e: React.MouseEvent) => {
		e.stopPropagation();
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

	const isAllSelected = selectedIds.size === virtualKeys.length && virtualKeys.length > 0;

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

			<div className="space-y-4">
				<div className="flex items-center justify-between">
					<div>
						<h2 className="text-lg font-semibold">Virtual Keys</h2>
						<p className="text-muted-foreground text-sm">Manage virtual keys, their permissions, budgets, and rate limits.</p>
					</div>
					<Button onClick={handleAddVirtualKey} disabled={!hasCreateAccess} data-testid="create-vk-btn">
						<Plus className="h-4 w-4" />
						Add Virtual Key
					</Button>
				</div>

				{selectedIds.size > 0 && (
					<div className="flex items-center justify-between bg-secondary border border-border rounded-md px-4 py-3">
						<span className="text-sm font-medium">
							{selectedIds.size} key{selectedIds.size !== 1 ? 's' : ''} selected
						</span>
						<AlertDialog open={showBulkDeleteDialog} onOpenChange={setShowBulkDeleteDialog}>
							<AlertDialogTrigger asChild>
								<Button 
									variant="destructive" 
									size="sm" 
									disabled={!hasDeleteAccess}
									data-testid="bulk-delete-btn"
								>
									<Trash2 className="h-4 w-4 mr-2" />
									Delete
								</Button>
							</AlertDialogTrigger>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Delete Virtual Keys</AlertDialogTitle>
									<AlertDialogDescription>
										Are you sure you want to delete {selectedIds.size} virtual key{selectedIds.size !== 1 ? 's' : ''}? This action cannot be undone.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction 
										onClick={handleBulkDelete} 
										disabled={isBulkDeleting}
										data-testid="confirm-bulk-delete-btn"
									>
										{isBulkDeleting ? "Deleting..." : "Delete"}
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					</div>
				)}

				{/* Toolbar: Search + Filters */}
				<div className="flex items-center gap-3">
					<div className="relative max-w-sm flex-1">
						<Search className="text-muted-foreground absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label="Search virtual keys by name"
							placeholder="Search by name..."
							value={search}
							onChange={(e) => onSearchChange(e.target.value)}
							className="pl-9"
							data-testid="vk-search-input"
						/>
					</div>
					<Select value={customerFilter} onValueChange={(val) => onCustomerFilterChange(val === "all" ? "" : val)}>
						<SelectTrigger className="w-[180px]" data-testid="vk-customer-filter">
							<SelectValue placeholder="All Customers" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="all">All Customers</SelectItem>
							{customers.map((c) => (
								<SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>
							))}
						</SelectContent>
					</Select>
					{customerFilter && teamFilter && (
						<span className="text-muted-foreground text-xs font-medium">or</span>
					)}
					<Select value={teamFilter} onValueChange={(val) => onTeamFilterChange(val === "all" ? "" : val)}>
						<SelectTrigger className="w-[180px]" data-testid="vk-team-filter">
							<SelectValue placeholder="All Teams" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="all">All Teams</SelectItem>
							{teams.map((t) => (
								<SelectItem key={t.id} value={t.id}>{t.name}</SelectItem>
							))}
						</SelectContent>
					</Select>
				</div>

				<div className="rounded-sm border">
					<Table data-testid="vk-table">
						<TableHeader>
							<TableRow>
								<TableHead className="w-12">
									<Checkbox
										checked={isAllSelected}
										onCheckedChange={toggleSelectAll}
										aria-label="Select all virtual keys"
										data-testid="vk-select-all-checkbox"
									/>
								</TableHead>
								<TableHead>Name</TableHead>
								<TableHead>Assigned To</TableHead>
								<TableHead>Key</TableHead>
								<TableHead>Budget</TableHead>
								<TableHead>Status</TableHead>
								<TableHead className="text-right"></TableHead>
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
									const isSelected = selectedIds.has(vk.id);
									const isExhausted =
										(vk.budget?.current_usage && vk.budget?.max_limit && vk.budget.current_usage >= vk.budget.max_limit) ||
										(vk.rate_limit?.token_current_usage &&
											vk.rate_limit?.token_max_limit &&
											vk.rate_limit.token_current_usage >= vk.rate_limit.token_max_limit) ||
										(vk.rate_limit?.request_current_usage &&
											vk.rate_limit?.request_max_limit &&
											vk.rate_limit.request_current_usage >= vk.rate_limit.request_max_limit);

									return (
										<TableRow
											key={vk.id}
											data-testid={`vk-row-${vk.name}`}
											className={cn(
												"hover:bg-muted/50 transition-colors",
												isSelected && "bg-secondary"
											)}
										>
											<TableCell 
												className="w-12" 
												onClick={(e) => {
													e.stopPropagation();
													toggleRowSelection(vk.id);
												}}
											>
												<Checkbox
													checked={isSelected}
													onCheckedChange={() => toggleRowSelection(vk.id)}
													aria-label={`Select ${vk.name}`}
													data-testid={`vk-checkbox-${vk.name}`}
												/>
											</TableCell>
											<TableCell 
												className="max-w-[200px] cursor-pointer" 
												onClick={() => handleRowClick(vk)}
											>
												<div className="truncate font-medium">{vk.name}</div>
											</TableCell>
											<TableCell>
												{vk.team ? (
													<Badge variant="outline">Team: {vk.team.name}</Badge>
												) : vk.customer ? (
													<Badge variant="outline">Customer: {vk.customer.name}</Badge>
												) : (
													<span className="text-muted-foreground text-sm">-</span>
												)}
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center gap-2">
													<code className="cursor-default px-2 py-1 font-mono text-sm" data-testid="vk-key-value">{maskKey(vk.value, isRevealed)}</code>
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
											</TableCell>
										<TableCell>
											{vk.budget ? (
												<div className="flex flex-col gap-0.5">
													<span className={cn("font-mono text-sm", vk.budget.current_usage >= vk.budget.max_limit && "text-red-400")}>
														{formatCurrency(vk.budget.current_usage)} / {formatCurrency(vk.budget.max_limit)}
													</span>
													<span className="text-muted-foreground text-xs">
														Resets {formatResetDuration(vk.budget.reset_duration)}
														{vk.budget.calendar_aligned && " (calendar)"}
													</span>
												</div>
											) : (
												<span className="text-muted-foreground text-sm">-</span>
											)}
										</TableCell>
											<TableCell>
												<Badge variant={vk.is_active ? (isExhausted ? "destructive" : "default") : "secondary"}>
													{vk.is_active ? (isExhausted ? "Exhausted" : "Active") : "Inactive"}
												</Badge>
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
													<AlertDialog>
														<AlertDialogTrigger asChild>
															<Button
																variant="ghost"
																size="sm"
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
																	Are you sure you want to delete &quot;{vk.name.length > 20 ? `${vk.name.slice(0, 20)}...` : vk.name}
																	&quot;? This action cannot be undone.
																</AlertDialogDescription>
															</AlertDialogHeader>
															<AlertDialogFooter>
																<AlertDialogCancel>Cancel</AlertDialogCancel>
																<AlertDialogAction onClick={() => handleDelete(vk.id)} disabled={isDeleting}>
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
