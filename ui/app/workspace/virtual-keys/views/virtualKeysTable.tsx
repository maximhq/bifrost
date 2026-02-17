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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { getErrorMessage, useDeleteVirtualKeyMutation } from "@/lib/store"
import { Customer, Team, VirtualKey } from "@/lib/types/governance"
import { cn } from "@/lib/utils"
import { formatCurrency } from "@/lib/utils/governance"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { Copy, Edit, Eye, EyeOff, Plus, Trash2 } from "lucide-react"
import { useMemo, useState } from "react"
import { toast } from "sonner"
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet"
import VirtualKeySheet from "./virtualKeySheet"

interface VirtualKeysTableProps {
	virtualKeys: VirtualKey[];
	teams: Team[];
	customers: Customer[];
}

export default function VirtualKeysTable({ virtualKeys, teams, customers }: VirtualKeysTableProps) {
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
			for (const vkId of keysToDelete) {
				await deleteVirtualKey(vkId).unwrap();
			}
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

	const copyToClipboard = (key: string) => {
		navigator.clipboard.writeText(key);
		toast.success("Copied to clipboard");
	};

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
					<div className="flex items-center justify-between bg-blue-50 dark:bg-blue-950 border border-blue-200 dark:border-blue-800 rounded-md px-4 py-3">
						<span className="text-sm font-medium text-blue-900 dark:text-blue-100">
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
								<TableHead>Key</TableHead>
								<TableHead>Budget</TableHead>
								<TableHead>Status</TableHead>
								<TableHead className="text-right"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{virtualKeys?.length === 0 ? (
								<TableRow>
									<TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
										No virtual keys found. Create your first virtual key to get started.
									</TableCell>
								</TableRow>
							) : (
								virtualKeys?.map((vk) => {
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
												isSelected && "bg-blue-50 dark:bg-blue-950"
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
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center gap-2">
													<code className="cursor-default px-2 py-1 font-mono text-sm">{maskKey(vk.value, isRevealed)}</code>
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
													<span className={cn("font-mono text-sm", vk.budget.current_usage >= vk.budget.max_limit && "text-red-400")}>
														{formatCurrency(vk.budget.current_usage)} / {formatCurrency(vk.budget.max_limit)}
													</span>
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
			</div>
		</>
	);
}
