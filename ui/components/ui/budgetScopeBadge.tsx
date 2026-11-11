import { Badge } from "@/components/ui/badge";
import type { LimitScope } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

interface BudgetScopeBadgeProps {
	/** Undefined reads as the Shared default, "shared". */
	scope?: LimitScope;
	className?: string;
}

/**
 * Marks which traffic a budget line is charged by: Shared, Model or MCP.
 *
 * Outlined and small so it reads as a property of the budget line, apart from the filled badges
 * tables use for a limit's governance scope (Global, Virtual Key). Rendered only where budgets can
 * carry a scope; provider- and model-specific budgets are always shared and show none.
 */
export default function BudgetScopeBadge({ scope, className }: BudgetScopeBadgeProps) {
	const { t } = useTranslation("common");
	const value = scope || "shared";
	return (
		<Badge
			variant="outline"
			title={t(`budgetScope.${value}Hint`)}
			className={cn("h-4 shrink-0 px-1 text-[10px] leading-none font-normal", className)}
			data-testid={`budget-scope-badge-${value}`}
		>
			{t(`budgetScope.${value}`)}
		</Badge>
	);
}