/**
 * Routing Rules Table
 * Displays all routing rules with CRUD actions
 */

"use client";

import { RoutingRule } from "@/lib/types/routingRules";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
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
import { ChevronLeft, ChevronRight, Edit, Search, Trash2 } from "lucide-react";
import { truncateCELExpression, getScopeLabel, getPriorityBadgeClass } from "@/lib/utils/routingRules";
import { useEffect, useMemo, useState } from "react";
import { useDeleteRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import { toast } from "sonner";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage } from "@/lib/store";
import { RoutingTarget } from "@/lib/types/routingRules";

interface RoutingRulesTableProps {
	rules: RoutingRule[] | undefined;
	totalCount: number;
	isLoading: boolean;
	onEdit: (rule: RoutingRule) => void;
	/** When false, delete button is hidden and deletion is disabled (e.g. for read-only users). */
	canDelete?: boolean;
	search: string;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export function RoutingRulesTable({
	rules,
	totalCount,
	isLoading,
	onEdit,
	canDelete = false,
	search,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
}: RoutingRulesTableProps) {
	const [deleteRuleId, setDeleteRuleId] = useState<string | null>(null);
	const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
	const [showBulkDeleteDialog, setShowBulkDeleteDialog] = useState(false);
	const [isBulkDeleting, setIsBulkDeleting] = useState(false);
	const [deleteRoutingRule, { isLoading: isDeleting }] = useDeleteRoutingRuleMutation();
	const sortedRules = useMemo(() => (rules ? [...rules].sort((a, b) => a.priority - b.priority) : []), [rules]);
	const isAllSelected = selectedIds.size === sortedRules.length && sortedRules.length > 0;

	const handleDelete = async () => {
		if (!canDelete || !deleteRuleId) return;

		try {
			await deleteRoutingRule(deleteRuleId).unwrap();
			toast.success("Routing rule deleted successfully");
			setDeleteRuleId(null);
		} catch (error: any) {
			toast.error(getErrorMessage(error));
		}
	};

	const toggleRowSelection = (ruleId: string) => {
		setSelectedIds((previous) => {
			const next = new Set(previous);
			if (next.has(ruleId)) {
				next.delete(ruleId);
			} else {
				next.add(ruleId);
			}
			return next;
		});
	};

	const toggleSelectAll = () => {
		if (selectedIds.size === sortedRules.length && sortedRules.length > 0) {
			setSelectedIds(new Set());
			return;
		}
		setSelectedIds(new Set(sortedRules.map((rule) => rule.id)));
	};

	const handleBulkDelete = async () => {
		if (!canDelete || isBulkDeleting || selectedIds.size === 0) return;

		setIsBulkDeleting(true);
		try {
			const ruleIds = Array.from(selectedIds);
			let deletedCount = 0;
			const failedIds: string[] = [];

			for (const ruleId of ruleIds) {
				try {
					await deleteRoutingRule(ruleId).unwrap();
					deletedCount += 1;
				} catch {
					failedIds.push(ruleId);
				}
			}

			if (deletedCount > 0) {
				toast.success(`${deletedCount} routing rule(s) deleted successfully`);
			}

			if (failedIds.length > 0) {
				toast.error(
					deletedCount > 0 ? `${failedIds.length} routing rule(s) could not be deleted.` : "Failed to delete the selected routing rules.",
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

	useEffect(() => {
		const visibleIDs = new Set(sortedRules.map((rule) => rule.id));
		setSelectedIds((previous) => new Set(Array.from(previous).filter((id) => visibleIDs.has(id))));
	}, [sortedRules]);

	if (isLoading) {
		return (
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="w-12"></TableHead>
							<TableHead>Name</TableHead>
							<TableHead>Targets</TableHead>
							<TableHead>Scope</TableHead>
							<TableHead className="text-right">Priority</TableHead>
							<TableHead>Expression</TableHead>
							<TableHead>Status</TableHead>
							<TableHead className="text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{[...Array(5)].map((_, i) => (
							<TableRow key={i}>
								<TableCell colSpan={8} className="h-10">
									<div className="bg-muted h-2 w-32 animate-pulse rounded" />
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		);
	}

	const ruleToDelete = sortedRules.find((r) => r.id === deleteRuleId);

	return (
		<>
			{/* Toolbar: Search */}
			<div className="flex items-center gap-3">
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label="Search routing rules by name"
						placeholder="Search by name..."
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="routing-rules-search-input"
					/>
				</div>
			</div>

			{selectedIds.size > 0 && (
				<div className="border-border bg-secondary flex items-center justify-between rounded-md border px-4 py-3">
					<span className="text-sm font-medium">
						{selectedIds.size} routing rule{selectedIds.size !== 1 ? "s" : ""} selected
					</span>
					<AlertDialog
						open={showBulkDeleteDialog}
						onOpenChange={(open) => {
							if (!open && isBulkDeleting) return;
							setShowBulkDeleteDialog(open);
						}}
					>
						<Button
							variant="destructive"
							size="sm"
							onClick={() => setShowBulkDeleteDialog(true)}
							disabled={!canDelete || isBulkDeleting}
							data-testid="routing-rules-bulk-delete-btn"
						>
							<Trash2 className="mr-2 h-4 w-4" />
							Delete
						</Button>
						<AlertDialogContent>
							<AlertDialogHeader>
								<AlertDialogTitle>Delete Routing Rules</AlertDialogTitle>
								<AlertDialogDescription>
									Are you sure you want to delete {selectedIds.size} routing rule{selectedIds.size !== 1 ? "s" : ""}? This action cannot be
									undone.
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
									className="bg-destructive hover:bg-destructive/90"
									data-testid="routing-rules-confirm-bulk-delete-btn"
								>
									{isBulkDeleting ? "Deleting..." : "Delete"}
								</AlertDialogAction>
							</AlertDialogFooter>
						</AlertDialogContent>
					</AlertDialog>
				</div>
			)}

			<div className="overflow-hidden rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow className="bg-muted/50">
							<TableHead className="w-12">
								<Checkbox
									checked={isAllSelected}
									onCheckedChange={toggleSelectAll}
									aria-label="Select all routing rules"
									data-testid="routing-rules-select-all-checkbox"
								/>
							</TableHead>
							<TableHead className="font-semibold">Name</TableHead>
							<TableHead className="font-semibold">Targets</TableHead>
							<TableHead className="font-semibold">Scope</TableHead>
							<TableHead className="text-right font-semibold">Priority</TableHead>
							<TableHead className="font-semibold">Expression</TableHead>
							<TableHead className="font-semibold">Status</TableHead>
							<TableHead className="text-right font-semibold">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{sortedRules.length === 0 ? (
							<TableRow>
								<TableCell colSpan={8} className="h-24 text-center">
									<span className="text-muted-foreground text-sm">No matching routing rules found.</span>
								</TableCell>
							</TableRow>
						) : (
							sortedRules.map((rule) => (
								<TableRow
									key={rule.id}
									data-testid={`routing-rule-row-${rule.name}`}
									className="hover:bg-muted/50 cursor-pointer transition-colors"
								>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<Checkbox
											checked={selectedIds.has(rule.id)}
											onCheckedChange={() => toggleRowSelection(rule.id)}
											aria-label={`Select routing rule ${rule.name}`}
											data-testid={`routing-rule-checkbox-${rule.name}`}
										/>
									</TableCell>
									<TableCell className="font-medium">
										<div className="flex flex-col gap-1">
											<span className="max-w-xs truncate">{rule.name}</span>
											{rule.description && (
												<span data-testid="routing-rule-description" className="text-muted-foreground max-w-xs truncate text-xs">
													{rule.description}
												</span>
											)}
										</div>
									</TableCell>
									<TableCell>
										<TargetsSummary targets={rule.targets || []} />
									</TableCell>
									<TableCell>
										<Badge variant="secondary">{getScopeLabel(rule.scope)}</Badge>
									</TableCell>
									<TableCell className="text-right">
										<div className={`inline-block rounded px-2.5 py-1 text-xs font-medium ${getPriorityBadgeClass(rule.priority)}`}>
											{rule.priority}
										</div>
									</TableCell>
									<TableCell>
										<span className="text-muted-foreground block max-w-xs truncate font-mono text-xs" title={rule.cel_expression}>
											{truncateCELExpression(rule.cel_expression)}
										</span>
									</TableCell>
									<TableCell>
										<Badge variant={rule.enabled ? "default" : "secondary"}>{rule.enabled ? "Enabled" : "Disabled"}</Badge>
									</TableCell>
									<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
										<div className="flex items-center justify-end gap-2">
											<Button variant="ghost" size="sm" onClick={() => onEdit(rule)} aria-label="Edit routing rule">
												<Edit className="h-4 w-4" />
											</Button>
											{canDelete && (
												<Button variant="ghost" size="sm" onClick={() => setDeleteRuleId(rule.id)} aria-label="Delete routing rule">
													<Trash2 className="h-4 w-4" />
												</Button>
											)}
										</div>
									</TableCell>
								</TableRow>
							))
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
							data-testid="routing-rules-pagination-prev-btn"
						>
							<ChevronLeft className="mr-1 h-4 w-4" />
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							disabled={offset + limit >= totalCount}
							onClick={() => onOffsetChange(offset + limit)}
							data-testid="routing-rules-pagination-next-btn"
						>
							Next
							<ChevronRight className="ml-1 h-4 w-4" />
						</Button>
					</div>
				</div>
			)}

			<AlertDialog open={!!deleteRuleId} onOpenChange={(open) => !open && setDeleteRuleId(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Routing Rule</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete &quot;{ruleToDelete?.name}&quot;? This action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={isDeleting}>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={handleDelete} disabled={isDeleting} className="bg-destructive hover:bg-destructive/90">
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}

function TargetsSummary({ targets }: { targets: RoutingTarget[] }) {
	if (!targets || targets.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}

	const first = targets[0];
	const label = [first.provider ? getProviderLabel(first.provider) : "Any", first.model || "Any model"].join(" / ");

	return (
		<div className="flex flex-col gap-1">
			<div className="flex items-center gap-1.5">
				{first.provider && <RenderProviderIcon provider={first.provider as ProviderIconType} size="sm" className="h-4 w-4 shrink-0" />}
				<span className="max-w-[160px] truncate text-sm">{label}</span>
				{targets.length === 1 && <span className="text-muted-foreground shrink-0 text-xs">{first.weight}</span>}
			</div>
			{targets.length > 1 && (
				<span className="text-muted-foreground text-xs">
					+{targets.length - 1} more target{targets.length > 2 ? "s" : ""}
				</span>
			)}
		</div>
	);
}
