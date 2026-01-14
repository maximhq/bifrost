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
} from "@/components/ui/alertDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { getErrorMessage, useDeleteVirtualKeyMutation } from "@/lib/store"
import { Customer, Team, VirtualKey } from "@/lib/types/governance"
import { cn } from "@/lib/utils"
import { formatCurrency } from "@/lib/utils/governance"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { Copy, Edit, Eye, EyeOff, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet"
import VirtualKeySheet from "./virtualKeySheet"

interface VirtualKeysTableProps {
	virtualKeys: VirtualKey[];
	teams: Team[];
	customers: Customer[];
	onRefresh: () => void;
}

export default function VirtualKeysTable({ virtualKeys, teams, customers, onRefresh }: VirtualKeysTableProps) {
  const [showVirtualKeySheet, setShowVirtualKeySheet] = useState(false)
  const [editingVirtualKey, setEditingVirtualKey] = useState<VirtualKey | null>(null)
  const [revealedKeys, setRevealedKeys] = useState<Set<string>>(new Set())
  const [selectedVirtualKey, setSelectedVirtualKey] = useState<VirtualKey | null>(null)
  const [showDetailSheet, setShowDetailSheet] = useState(false)

  const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create)
  const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update)
  const hasDeleteAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Delete)

  const [deleteVirtualKey, { isLoading: isDeleting }] = useDeleteVirtualKeyMutation()

	const handleDelete = async (vkId: string) => {
		try {
			await deleteVirtualKey(vkId).unwrap();
			toast.success("Virtual key deleted successfully");
			onRefresh();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddVirtualKey = () => {
		setEditingVirtualKey(null);
		setShowVirtualKeySheet(true);
	};

	const handleEditVirtualKey = (vk: VirtualKey, e: React.MouseEvent) => {
		e.stopPropagation(); // Prevent row click
		setEditingVirtualKey(vk);
		setShowVirtualKeySheet(true);
	};

	const handleVirtualKeySaved = () => {
		setShowVirtualKeySheet(false);
		setEditingVirtualKey(null);
		onRefresh();
	};

	const handleRowClick = (vk: VirtualKey) => {
		setSelectedVirtualKey(vk);
		setShowDetailSheet(true);
	};

	const handleDetailSheetClose = () => {
		setShowDetailSheet(false);
		setSelectedVirtualKey(null);
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
						<p className="text-muted-foreground text-sm">Manage virtual keys, their permissions, budgets, and rate limits.</p>
					</div>
					<Button onClick={handleAddVirtualKey} disabled={!hasCreateAccess}>
						<Plus className="h-4 w-4" />
						Add Virtual Key
					</Button>
				</div>

				<div className="rounded-sm border">
					<Table>
						<TableHeader>
							<TableRow>
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
									<TableCell colSpan={5} className="text-muted-foreground py-8 text-center">
										No virtual keys found. Create your first virtual key to get started.
									</TableCell>
								</TableRow>
							) : (
								virtualKeys?.map((vk) => {
									const isRevealed = revealedKeys.has(vk.id);
									const isExhausted =
										(vk.budget?.current_usage && vk.budget?.max_limit && vk.budget.current_usage >= vk.budget.max_limit) ||
										(vk.rate_limit?.token_current_usage &&
											vk.rate_limit?.token_max_limit &&
											vk.rate_limit.token_current_usage >= vk.rate_limit.token_max_limit) ||
										(vk.rate_limit?.request_current_usage &&
											vk.rate_limit?.request_max_limit &&
											vk.rate_limit.request_current_usage >= vk.rate_limit.request_max_limit);

									return (
										<TableRow key={vk.id} className="hover:bg-muted/50 cursor-pointer transition-colors" onClick={() => handleRowClick(vk)}>
											<TableCell className="max-w-[200px]">
												<div className="truncate font-medium">{vk.name}</div>
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center gap-2">
													<code className="cursor-default px-2 py-1 font-mono text-sm">{maskKey(vk.value, isRevealed)}</code>
													<Button variant="ghost" size="sm" onClick={() => toggleKeyVisibility(vk.id)}>
														{isRevealed ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
													</Button>
													<Button variant="ghost" size="sm" onClick={() => copyToClipboard(vk.value)}>
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
													<Button variant="ghost" size="sm" onClick={(e) => handleEditVirtualKey(vk, e)} disabled={!hasUpdateAccess}>
														<Edit className="h-4 w-4" />
													</Button>
													<AlertDialog>
														<AlertDialogTrigger asChild>
															<Button variant="ghost" size="sm" onClick={(e) => e.stopPropagation()} disabled={!hasDeleteAccess}>
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
