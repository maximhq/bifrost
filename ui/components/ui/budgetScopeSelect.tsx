import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { BUDGET_SCOPE_VALUES, DEFAULT_BUDGET_SCOPE } from "@/lib/constants/governance";
import type { LimitScope } from "@/lib/types/governance";
import { useTranslation } from "react-i18next";

interface BudgetScopeSelectProps {
	"data-testid"?: string;
	id?: string;
	/** Undefined reads as the shared default, "shared". */
	value?: LimitScope;
	onChange: (scope: LimitScope) => void;
	labelClassName?: string;
	/** The scopes to offer; every scope when omitted. */
	options?: readonly LimitScope[];
}

/**
 * Which traffic a budget is charged by: model requests, MCP tool executions, or both (shared).
 *
 * Shared by every budget editor that offers a scope, and sized to sit beside the Reset
 * Period select of NumberAndSelect (same label row, w-40 column), so a budget line reads
 * as amount · period · scope.
 */
export default function BudgetScopeSelect({
	"data-testid": testId,
	id,
	value,
	onChange,
	labelClassName,
	options = BUDGET_SCOPE_VALUES,
}: BudgetScopeSelectProps) {
	const { t } = useTranslation("common");
	return (
		<div className="w-40 shrink-0 space-y-2" data-testid={testId}>
			<Label htmlFor={id} className={labelClassName}>
				{t("budgetScope.label")}
			</Label>
			<Select value={value ?? DEFAULT_BUDGET_SCOPE} onValueChange={(scope) => onChange(scope as LimitScope)}>
				<SelectTrigger id={id} className="m-0 w-full" data-testid={testId ? `${testId}-trigger` : undefined}>
					<SelectValue />
				</SelectTrigger>
				<SelectContent>
					{options.map((scope) => (
						<SelectItem key={scope} value={scope} data-testid={testId ? `${testId}-option-${scope}` : undefined}>
							{t(`budgetScope.${scope}`)}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</div>
	);
}