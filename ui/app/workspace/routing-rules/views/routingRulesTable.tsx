/**
 * Routing Rules Table
 * Displays all routing rules with CRUD actions
 */

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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage } from "@/lib/store";
import { useDeleteRoutingRuleMutation, useUpdateRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import { RoutingRule, RoutingTarget } from "@/lib/types/routingRules";
import { getScopeLabel } from "@/lib/utils/labels";
import { getPriorityBadgeClass, truncateCELExpression } from "@/lib/utils/routingRules";
import { ChevronLeft, ChevronRight, Edit, MoreHorizontal, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

function RoutingRuleActionsMenu({
	rule,
	canUpdate,
	canDelete,
	onEdit,
	onDelete,
}: {
	rule: RoutingRule;
	canUpdate: boolean;
	canDelete: boolean;
	onEdit: (rule: RoutingRule) => void;
	onDelete: (ruleId: string) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label={`Actions for routing rule ${rule.name}`}
					data-testid={`routing-rule-actions-${rule.id}-btn`}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					className="cursor-pointer"
					disabled={!canUpdate}
					data-testid={`routing-rule-edit-${rule.id}-btn`}
					onSelect={(e) => {
						e.preventDefault();
						onEdit(rule);
						setIsOpen(false);
					}}
				>
					<Edit className="h-4 w-4" />
					Edit
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					disabled={!canDelete}
					data-testid={`routing-rule-delete-${rule.id}-btn`}
					onSelect={(e) => {
						e.preventDefault();
						onDelete(rule.id);
						setIsOpen(false);
					}}
				>
					<Trash2 className="h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface RoutingRulesTableProps {
	rules: RoutingRule[] | undefined;
	totalCount: number;
	isLoading: boolean;
	onEdit: (rule: RoutingRule) => void;
	onRowClick: (rule: RoutingRule) => void;
	/** When false, delete button is hidden and deletion is disabled (e.g. for read-only users). */
	canDelete?: boolean;
	/** When false, enabled toggle is disabled (e.g. for read-only users). */
	canUpdate?: boolean;
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
	onRowClick,
	canDelete = false,
	canUpdate = false,
	search,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
}: RoutingRulesTableProps) {
	const [deleteRuleId, setDeleteRuleId] = useState<string | null>(null);
	const [deleteRoutingRule, { isLoading: isDeleting }] = useDeleteRoutingRuleMutation();
	const [updateRoutingRule] = useUpdateRoutingRuleMutation();

	const handleDelete = async () => {
		if (!canDelete || !deleteRuleId) return;

		try {
			await deleteRoutingRule(deleteRuleId).unwrap();
			toast.success("Routing rule deleted successfully");
			setDeleteRuleId(null);
		} catch (error: unknown) {
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
							<TableHead>Enabled</TableHead>
							<TableHead className="text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{[...Array(5)].map((_, i) => (
							<TableRow key={i}>
								<TableCell colSpan={7} className="h-10">
									<div className="bg-muted h-2 w-32 animate-pulse rounded" />
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
			<div className="mb-4 flex items-center gap-3">
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

			<div className="mb-2 overflow-hidden rounded-sm border">
				<Table containerClassName="h-full overflow-auto">
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow className="bg-muted/50">
							<TableHead className="font-semibold">Name</TableHead>
							<TableHead className="font-semibold">Targets</TableHead>
							<TableHead className="font-semibold">Scope</TableHead>
							<TableHead className="text-right font-semibold">Priority</TableHead>
							<TableHead className="font-semibold">Expression</TableHead>
							<TableHead className="font-semibold">Status</TableHead>
							<TableHead className={`bg-muted sticky right-0 z-30 w-[50px] text-right font-semibold ${PIN_SHADOW_RIGHT}`}>
								Actions
							</TableHead>
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
								<TableRow
									key={rule.id}
									className="group hover:bg-muted/50 cursor-pointer transition-colors"
									onClick={() => onRowClick(rule)}
								>
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
										<div className={`inline-block rounded px-2.5 py-1 text-xs font-medium ${getPriorityBadgeClass()}`}>{rule.priority}</div>
									</TableCell>
									<TableCell>
										<span className="text-muted-foreground block max-w-xs truncate font-mono text-xs" title={rule.cel_expression}>
											{truncateCELExpression(rule.cel_expression)}
										</span>
									</TableCell>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<Switch
											data-testid={`routing-rule-enabled-${rule.id}-switch`}
											checked={rule.enabled ?? true}
											size="md"
											disabled={!canUpdate}
											onAsyncCheckedChange={async (checked) => {
												await updateRoutingRule({
													id: rule.id,
													data: { enabled: checked },
												})
													.unwrap()
													.then(() => {
														toast.success(`Rule ${checked ? "enabled" : "disabled"} successfully`);
													})
													.catch((err) => {
														toast.error("Failed to update rule", {
															description: getErrorMessage(err),
														});
													});
											}}
										/>
									</TableCell>
									<TableCell
										className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
										onClick={(e) => e.stopPropagation()}
									>
										<div className="flex items-center justify-center">
											<RoutingRuleActionsMenu
												rule={rule}
												canUpdate={canUpdate}
												canDelete={canDelete}
												onEdit={onEdit}
												onDelete={setDeleteRuleId}
											/>
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
				<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
					<div className="text-muted-foreground flex items-center gap-2">
						{(offset + 1).toLocaleString()}-{Math.min(offset + limit, totalCount).toLocaleString()} of {totalCount.toLocaleString()} entries
					</div>

					<div className="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							onClick={() => onOffsetChange(Math.max(0, offset - limit))}
							disabled={offset === 0}
							data-testid="routing-rules-pagination-prev-btn"
							aria-label="Previous page"
						>
							<ChevronLeft className="size-3" />
						</Button>

						<div className="flex items-center gap-1">
							<span>Page</span>
							<span>{Math.floor(offset / limit) + 1}</span>
							<span>of {Math.ceil(totalCount / limit)}</span>
						</div>

						<Button
							variant="ghost"
							size="sm"
							onClick={() => onOffsetChange(offset + limit)}
							disabled={offset + limit >= totalCount}
							data-testid="routing-rules-pagination-next-btn"
							aria-label="Next page"
						>
							<ChevronRight className="size-3" />
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
			</div>
			{targets.length > 1 && (
				<span className="text-muted-foreground text-xs">
					+{targets.length - 1} more target{targets.length > 2 ? "s" : ""}
				</span>
			)}
		</div>
	);
}