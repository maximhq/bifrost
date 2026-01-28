/**
 * Routing Rules View
 * Main orchestrator component for routing rules management
 */

"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Plus } from "lucide-react";
import { RoutingRule } from "@/lib/types/routingRules";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { RoutingRulesTable } from "./routingRulesTable";
import { RoutingRuleSheet } from "./routingRuleSheet";
import { useRbac } from "@/app/_fallbacks/enterprise/lib/contexts/rbacContext";
import { RbacResource, RbacOperation } from "@/app/_fallbacks/enterprise/lib/contexts/rbacContext";

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

	return (
		<div className="space-y-4">
			{/* Header */}
			<div className="flex items-center justify-between">
				<div>
					<p className="text-muted-foreground text-sm">
						Manage CEL-based routing rules for intelligent request routing across providers
					</p>
				</div>
				{canCreate && (
					<Button
						onClick={handleCreateNew}
						disabled={isLoading}
						className="gap-2"
					>
						<Plus className="h-4 w-4" />
						<span className="hidden sm:inline">New Rule</span>
					</Button>
				)}
			</div>

			{/* Empty state or Table */}
			{!isLoading && rules.length === 0 ? (
				<div className="rounded-lg border border-dashed p-12 text-center">
					<Plus className="h-12 w-12 text-muted-foreground mx-auto mb-4" />
					<h3 className="text-lg font-semibold mb-1">
						No routing rules yet
					</h3>
					<p className="text-muted-foreground mb-6">
						Create your first routing rule to start intelligently routing requests
					</p>
					{canCreate && (
						<Button
							onClick={handleCreateNew}
							className="gap-2"
						>
							<Plus className="h-4 w-4" />
							Create First Rule
						</Button>
					)}
				</div>
			) : (
				<RoutingRulesTable
					rules={rules}
					isLoading={isLoading}
					onEdit={handleEdit}
					canDelete={canDelete}
				/>
			)}

			{/* RoutingRuleSheet */}
			<RoutingRuleSheet
				open={dialogOpen}
				onOpenChange={handleDialogOpenChange}
				editingRule={editingRule}
			/>
		</div>
	);
}
