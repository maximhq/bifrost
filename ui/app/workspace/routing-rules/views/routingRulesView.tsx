/**
 * Routing Rules View
 * Main orchestrator component for routing rules management
 */

import { Button } from "@/components/ui/button";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { RoutingRule } from "@/lib/types/routingRules";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { GitBranch, Plus } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { RoutingRuleInfoSheet } from "./routingRuleInfoSheet";
import { RoutingRuleSheet } from "./routingRuleSheet";
import { RoutingRulesEmptyState } from "./routingRulesEmptyState";
import { RoutingRulesTable } from "./routingRulesTable";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export function RoutingRulesView() {
	const [dialogOpen, setDialogOpen] = useState(false);
	const [editingRule, setEditingRule] = useState<RoutingRule | null>(null);
	const [infoSheetOpen, setInfoSheetOpen] = useState(false);
	const [selectedRule, setSelectedRule] = useState<RoutingRule | null>(null);

	const [search, setSearch] = useState("");
	const [offset, setOffset] = useState(0);

	const debouncedSearch = useDebouncedValue(search, 300);

	// Reset to first page when search changes
	useEffect(() => {
		setOffset(0);
	}, [debouncedSearch]);

	// Permissions
	const canCreate = useRbac(RbacResource.RoutingRules, RbacOperation.Create);
	const canDelete = useRbac(RbacResource.RoutingRules, RbacOperation.Delete);
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);

	// API
	const { data: rulesData, isLoading } = useGetRoutingRulesQuery(
		{
			limit: PAGE_SIZE,
			offset,
			search: debouncedSearch || undefined,
		},
		{
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const rules = rulesData?.rules || [];
	const totalCount = rulesData?.total_count || 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!rulesData || offset < totalCount) return;
		setOffset(totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE);
	}, [totalCount, offset]);

	const handleCreateNew = () => {
		setEditingRule(null);
		setDialogOpen(true);
	};

	const handleEdit = (rule: RoutingRule) => {
		setEditingRule(rule);
		setDialogOpen(true);
	};

	const handleRowClick = (rule: RoutingRule) => {
		setSelectedRule(rule);
		setInfoSheetOpen(true);
	};

	const sortedRules = useMemo(() => [...rules].sort((a, b) => a.priority - b.priority), [rules]);

	const selectedRuleIndex = useMemo(
		() => (selectedRule ? sortedRules.findIndex((r) => r.id === selectedRule.id) : -1),
		[selectedRule, sortedRules],
	);

	const handleRuleNavigate = useCallback(
		(direction: "prev" | "next") => {
			const newIndex = direction === "prev" ? selectedRuleIndex - 1 : selectedRuleIndex + 1;
			if (newIndex >= 0 && newIndex < sortedRules.length) {
				setSelectedRule(sortedRules[newIndex]);
			}
		},
		[selectedRuleIndex, sortedRules],
	);

	const handleDialogOpenChange = (open: boolean) => {
		setDialogOpen(open);
		if (!open) {
			setEditingRule(null);
		}
	};

	const hasActiveFilters = debouncedSearch;

	// True empty state: no rules at all (not just filtered to zero)
	if (!isLoading && totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				<RoutingRulesEmptyState onAddClick={handleCreateNew} canCreate={canCreate} />
				<RoutingRuleSheet open={dialogOpen} onOpenChange={handleDialogOpenChange} editingRule={editingRule} />
			</>
		);
	}

	return (
		<div className="flex flex-col overflow-y-auto">
			{/* Header */}
			<div className="mb-4 flex items-center justify-between">
				<div>
					<h1 className="text-foreground text-lg font-semibold">Routing Rules</h1>
					<p className="text-muted-foreground text-sm">Manage CEL-based routing rules for intelligent request routing across providers</p>
				</div>
				<div className="flex items-center gap-2">
					<Button variant="outline" size="sm" asChild className="gap-2">
						<Link to="/workspace/routing-rules/tree">
							<GitBranch className="h-4 w-4" />
							<span className="hidden sm:inline">View Tree</span>
						</Link>
					</Button>
					{canCreate && (
						<Button data-testid="create-routing-rule-btn" onClick={handleCreateNew} disabled={isLoading} className="gap-2">
							<Plus className="h-4 w-4" />
							<span className="hidden sm:inline">New Rule</span>
						</Button>
					)}
				</div>
			</div>

			<RoutingRulesTable
				rules={rules}
				totalCount={totalCount}
				isLoading={isLoading}
				onEdit={handleEdit}
				onRowClick={handleRowClick}
				canDelete={canDelete}
				canUpdate={canUpdate}
				search={search}
				onSearchChange={setSearch}
				offset={offset}
				limit={PAGE_SIZE}
				onOffsetChange={setOffset}
			/>

			<RoutingRuleSheet open={dialogOpen} onOpenChange={handleDialogOpenChange} editingRule={editingRule} />
			<RoutingRuleInfoSheet
				rule={selectedRule}
				open={infoSheetOpen}
				onOpenChange={setInfoSheetOpen}
				onNavigate={handleRuleNavigate}
				hasPrev={selectedRuleIndex > 0}
				hasNext={selectedRuleIndex >= 0 && selectedRuleIndex < sortedRules.length - 1}
			/>
		</div>
	);
}