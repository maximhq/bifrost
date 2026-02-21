/**
 * Routing Rules View
 * Main orchestrator component for routing rules management
 */

"use client";

import { RbacOperation, RbacResource, useRbac } from "@/app/_fallbacks/enterprise/lib/contexts/rbacContext";
import { Button } from "@/components/ui/button";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { RoutingRule } from "@/lib/types/routingRules";
import { Plus } from "lucide-react";
import { useState } from "react";
import { RoutingRuleSheet } from "./routingRuleSheet";
import { RoutingRulesEmptyState } from "./routingRulesEmptyState";
import { RoutingRulesTable } from "./routingRulesTable";

export function RoutingRulesView() {
	const [dialogOpen, setDialogOpen] = useState(false);
	const [editingRule, setEditingRule] = useState<RoutingRule | null>(null);

	// Permissions
	const canCreate = useRbac(RbacResource.RoutingRules, RbacOperation.Create);
	const canDelete = useRbac(RbacResource.RoutingRules, RbacOperation.Delete);

	// API
	const { data: rules = [], isLoading } = useGetRoutingRulesQuery();

	const handleCreateNew = () => {
		setEditingRule(null);
		setDialogOpen(true);
	};

	const handleEdit = (rule: RoutingRule) => {
		setEditingRule(rule);
		setDialogOpen(true);
	};

	const handleDialogOpenChange = (open: boolean) => {
		setDialogOpen(open);
		if (!open) {
			setEditingRule(null);
		}
	};

	// Empty state (same pattern as Plugins / MCP Servers): no header, just empty state + sheet
	if (!isLoading && rules.length === 0) {
		return (
			<>
				<RoutingRulesEmptyState onAddClick={handleCreateNew} canCreate={canCreate} />
				<RoutingRuleSheet open={dialogOpen} onOpenChange={handleDialogOpenChange} editingRule={editingRule} />
			</>
		);
	}

	return (
		<div className="space-y-4">
			{/* Header */}
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-foreground text-lg font-semibold">Routing Rules</h1>
					<p className="text-muted-foreground text-sm">Manage CEL-based routing rules for intelligent request routing across providers</p>
				</div>
				{canCreate && (
					<Button onClick={handleCreateNew} disabled={isLoading} className="gap-2">
						<Plus className="h-4 w-4" />
						<span className="hidden sm:inline">New Rule</span>
					</Button>
				)}
			</div>

			<RoutingRulesTable rules={rules} isLoading={isLoading} onEdit={handleEdit} canDelete={canDelete} />

			<RoutingRuleSheet open={dialogOpen} onOpenChange={handleDialogOpenChange} editingRule={editingRule} />
		</div>
	);
}
