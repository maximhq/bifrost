/**
 * Routing Rules Table
 * Displays all routing rules with CRUD actions
 */

"use client";

import { RoutingRule } from "@/lib/types/routingRules";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/components/ui/table";
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
import { useState } from "react";
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

export function RoutingRulesTable({ rules, totalCount, isLoading, onEdit, canDelete = false, search, onSearchChange, offset, limit, onOffsetChange }: RoutingRulesTableProps) {
	const [deleteRuleId, setDeleteRuleId] = useState<string | null>(null);
	const [deleteRoutingRule, { isLoading: isDeleting }] = useDeleteRoutingRuleMutation();

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

	if (isLoading) {
		return (
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow>
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
								<TableCell colSpan={7} className="h-10">
									<div className="h-2 w-32 bg-muted rounded animate-pulse" />
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		);
	}

	const sortedRules = rules ? [...rules].sort((a, b) => a.priority - b.priority) : [];
	const ruleToDelete = sortedRules.find((r) => r.id === deleteRuleId);

	return (
		<>
			{/* Toolbar: Search */}
			<div className="flex items-center gap-3">
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2" />
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

			<div className="rounded-sm border overflow-hidden">
				<Table>
					<TableHeader>
						<TableRow className="bg-muted/50">
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
								<TableCell colSpan={7} className="h-24 text-center">
									<span className="text-muted-foreground text-sm">No matching routing rules found.</span>
								</TableCell>
							</TableRow>
						) : (
						sortedRules.map((rule) => (
							<TableRow key={rule.id} className="hover:bg-muted/50 cursor-pointer transition-colors">
								<TableCell className="font-medium">
									<div className="flex flex-col gap-1">
										<span className="truncate max-w-xs">{rule.name}</span>
										{rule.description && (
											<span data-testid="routing-rule-description" className="text-xs text-muted-foreground truncate max-w-xs">{rule.description}</span>
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
									<div className={`inline-block px-2.5 py-1 rounded text-xs font-medium ${getPriorityBadgeClass(rule.priority)}`}>
										{rule.priority}
									</div>
								</TableCell>
								<TableCell>
									<span className="font-mono text-xs text-muted-foreground truncate max-w-xs block" title={rule.cel_expression}>
										{truncateCELExpression(rule.cel_expression)}
									</span>
								</TableCell>
								<TableCell>
									<Badge variant={rule.enabled ? "default" : "secondary"}>
										{rule.enabled ? "Enabled" : "Disabled"}
									</Badge>
								</TableCell>
								<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
									<div className="flex items-center justify-end gap-2">
										<Button variant="ghost" size="sm" onClick={() => onEdit(rule)} aria-label="Edit routing rule">
											<Edit className="h-4 w-4" />
										</Button>
										{canDelete && (
											<Button
												variant="ghost"
												size="sm"
												onClick={() => setDeleteRuleId(rule.id)}
												aria-label="Delete routing rule"
											>
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
	const label = [
		first.provider ? getProviderLabel(first.provider) : "Any",
		first.model || "Any model",
	].join(" / ");

	return (
		<div className="flex flex-col gap-1">
			<div className="flex items-center gap-1.5">
				{first.provider && (
					<RenderProviderIcon
						provider={first.provider as ProviderIconType}
						size="sm"
						className="h-4 w-4 shrink-0"
					/>
				)}
				<span className="text-sm truncate max-w-[160px]">{label}</span>
				{targets.length === 1 && (
					<span className="text-xs text-muted-foreground shrink-0">{first.weight}</span>
				)}
			</div>
			{targets.length > 1 && (
				<span className="text-xs text-muted-foreground">+{targets.length - 1} more target{targets.length > 2 ? "s" : ""}</span>
			)}
		</div>
	);
}
