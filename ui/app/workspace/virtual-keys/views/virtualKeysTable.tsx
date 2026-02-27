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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useDeleteVirtualKeyMutation } from "@/lib/store";
import { Customer, Team, VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Copy, Edit, Eye, EyeOff, Plus, SlidersHorizontal, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet";
import { VirtualKeysEmptyState } from "./virtualKeysEmptyState";
import VirtualKeySheet from "./virtualKeySheet";

interface VirtualKeysTableProps {
	virtualKeys: VirtualKey[];
	teams: Team[];
	customers: Customer[];
}

export default function VirtualKeysTable({ virtualKeys, teams, customers }: VirtualKeysTableProps) {
	const router = useRouter();
	const [showVirtualKeySheet, setShowVirtualKeySheet] = useState(false);
	const [editingVirtualKeyId, setEditingVirtualKeyId] = useState<string | null>(null);
	const [revealedKeys, setRevealedKeys] = useState<Set<string>>(new Set());
	const [selectedVirtualKeyId, setSelectedVirtualKeyId] = useState<string | null>(null);
	const [showDetailSheet, setShowDetailSheet] = useState(false);

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
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View);

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

	const copyToClipboard = (key: string) => {
		navigator.clipboard.writeText(key);
		toast.success("Copied to clipboard");
	};

	// Empty state when user has no virtual keys (same pattern as Plugins)
	if (virtualKeys?.length === 0) {
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

				<div className="rounded-sm border">
					<Table data-testid="vk-table">
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
							{virtualKeys?.map((vk) => {
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
									<TableRow
										key={vk.id}
										data-testid={`vk-row-${vk.name}`}
										className="hover:bg-muted/50 cursor-pointer transition-colors"
										onClick={() => handleRowClick(vk)}
									>
										<TableCell className="max-w-[200px]">
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
												<Button variant="ghost" size="sm" onClick={() => copyToClipboard(vk.value)} data-testid={`vk-copy-btn-${vk.name}`}>
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
													title="Pricing overrides"
													disabled={!hasSettingsAccess}
													onClick={(e) => {
														e.stopPropagation();
														router.push(
															`/workspace/custom-pricing/overrides?scope_kind=virtual_key&virtual_key_id=${encodeURIComponent(vk.id)}`,
														);
													}}
													data-testid={`vk-pricing-overrides-btn-${vk.name}`}
												>
													<SlidersHorizontal className="h-4 w-4" />
												</Button>
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
							})}
						</TableBody>
					</Table>
				</div>
			</div>
		</>
	);
}
