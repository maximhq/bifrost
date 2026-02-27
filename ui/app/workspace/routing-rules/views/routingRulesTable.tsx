/**
 * Routing Rules Table
 * Displays all routing rules with CRUD actions
 */

"use client";

import { RoutingRule } from "@/lib/types/routingRules";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import { Edit, Trash2, AlertCircle } from "lucide-react";
import { truncateCELExpression, getScopeLabel, getPriorityBadgeClass } from "@/lib/utils/routingRules";
import { useState } from "react";
import { useDeleteRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import { toast } from "sonner";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage } from "@/lib/store";

interface RoutingRulesTableProps {
	rules: RoutingRule[] | undefined;
	isLoading: boolean;
	onEdit: (rule: RoutingRule) => void;
	/** When false, delete button is hidden and deletion is disabled (e.g. for read-only users). */
	canDelete?: boolean;
}

export function RoutingRulesTable({ rules, isLoading, onEdit, canDelete = false }: RoutingRulesTableProps) {
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
							<TableHead>Provider</TableHead>
							<TableHead>Model</TableHead>
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
									<div className="h-2 w-32 bg-muted rounded animate-pulse" />
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		);
	}

	if (!rules || rules.length === 0) {
		return (
			<div className="rounded-sm border border-dashed p-8 text-center">
				<AlertCircle className="h-8 w-8 text-muted-foreground mx-auto mb-3" />
				<p className="font-medium">No routing rules yet</p>
				<p className="text-sm text-muted-foreground">Create your first rule to get started</p>
			</div>
		);
	}

	const sortedRules = [...rules].sort((a, b) => a.priority - b.priority);
	const ruleToDelete = sortedRules.find((r) => r.id === deleteRuleId);

	return (
		<>
			<div className="rounded-sm border overflow-hidden">
				<Table>
					<TableHeader>
						<TableRow className="bg-muted/50">
							<TableHead className="font-semibold">Name</TableHead>
							<TableHead className="font-semibold">Provider</TableHead>
							<TableHead className="font-semibold">Model</TableHead>
							<TableHead className="font-semibold">Scope</TableHead>
							<TableHead className="text-right font-semibold">Priority</TableHead>
							<TableHead className="font-semibold">Expression</TableHead>
							<TableHead className="font-semibold">Status</TableHead>
							<TableHead className="text-right font-semibold">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{sortedRules.map((rule) => (
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
									<div className="flex items-center gap-2">
										<RenderProviderIcon
											provider={rule.provider as ProviderIconType}
											size="sm"
											className="h-4 w-4"
										/>
										<span className="text-sm">{getProviderLabel(rule.provider || "-")}</span>
									</div>
								</TableCell>
								<TableCell className="text-sm">
									<span className="font-mono">{rule.model || "-"}</span>
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
						))}
					</TableBody>
				</Table>
			</div>

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
