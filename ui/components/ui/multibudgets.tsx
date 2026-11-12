import BudgetScopeSelect from "@/components/ui/budgetScopeSelect";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import QuarterStartSelect from "@/components/ui/quarterStartSelect";
import { budgetPeriodKey } from "@/lib/budgetOutline";
import { budgetResetDurationOptions, resetDurationOptions } from "@/lib/constants/governance";
import type { LimitScope } from "@/lib/types/governance";
import { Plus, RotateCcw, Trash2 } from "lucide-react";
import { useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";

export interface BudgetLineEntry {
	id?: string;
	max_limit?: number;
	reset_duration: string;
	reset_config?: { quarter_start_month?: number };
	scope?: LimitScope;
}

// Quarterly is the only period with settings beyond the duration itself.
const isQuarterly = (duration: string) => duration.endsWith("Q");

// The wording of a set of lines: spend budgets by default, request limits in RequestLimitLines.
export interface LineCopy {
	amountLabel: string;
	addLabel: string;
	emptyLabel: string;
	removeAriaLabel: (number: number) => string;
	duplicateLabel: string;
	duplicateScopeLabel: string;
}

export interface MultiBudgetLinesProps {
	"data-testid"?: string;
	label?: string;
	lines: BudgetLineEntry[];
	onChange: (lines: BudgetLineEntry[]) => void;
	options?: { label: string; value: string }[];
	onReset?: () => void;
	showReset?: boolean;
	// How many lines this holder can carry; Add Budget disappears once they are all there. Left out
	// where there is no ceiling - a team, a customer or a virtual key may carry one per reset window.
	maxLines?: number;
	// Offer the Shared / Model / MCP scope beside each line's reset period: on a key's own budgets, a
	// team, a customer and a model limit. Elsewhere every line stays the shared default and no option
	// is shown.
	showScope?: boolean;
	// Which scopes the selector offers when shown; every scope when omitted.
	scopeOptions?: readonly LimitScope[];
	// The scope a new line starts with when the selector is shown; the shared default when omitted.
	defaultScope?: LimitScope;
	// The reset period a new line starts with when it is unused; the first unused one when omitted.
	defaultDuration?: string;
	// Wording for lines that are not spend budgets (request limits); the budget strings when omitted.
	copy?: LineCopy;
	// Whole numbers only (request counts).
	integer?: boolean;
	// Inline error for a line's amount, shown under it (e.g. a request limit below 1).
	amountError?: (value: number | undefined) => string | undefined;
}

// pickNewLineDuration is the reset period a new line starts with: the preferred one when no line uses it,
// else the first unused one, else the first option.
export function pickNewLineDuration(options: { value: string }[], isUsed: (value: string) => boolean, preferred?: string): string {
	const available = options.find((o) => o.value === preferred && !isUsed(o.value)) ?? options.find((o) => !isUsed(o.value));
	return available?.value ?? options[0]?.value ?? "";
}

export default function MultiBudgetLines({
	"data-testid": testId,
	label: labelProp,
	lines,
	onChange,
	options = budgetResetDurationOptions,
	onReset,
	showReset,
	maxLines,
	showScope = false,
	scopeOptions,
	defaultScope,
	defaultDuration,
	copy,
	integer = false,
	amountError,
}: MultiBudgetLinesProps) {
	const { t } = useTranslation("governance");
	const { t: tModels } = useTranslation("models");
	const label = labelProp ?? tModels("virtualKeys.budgetConfiguration");
	const lineCopy: LineCopy = copy ?? {
		amountLabel: t("sheet.maximumSpend"),
		addLabel: t("sheet.addBudgetButton"),
		emptyLabel: t("sheet.noBudgetLimits"),
		removeAriaLabel: (number) => t("sheet.removeBudgetAria", { number }),
		duplicateLabel: t("sheet.duplicateResetPeriod"),
		duplicateScopeLabel: t("sheet.duplicateResetPeriodScope"),
	};
	// A hidden scope is submitted as the shared default, so it is counted as one.
	const periodKeyOf = (line: BudgetLineEntry) => budgetPeriodKey(line.reset_duration, showScope ? line.scope : undefined);
	// Track which reset period + scope pairs are already used (for duplicate detection)
	const usedPeriods = useMemo(() => {
		const counts = new Map<string, number>();
		for (const line of lines) {
			const key = periodKeyOf(line);
			counts.set(key, (counts.get(key) || 0) + 1);
		}
		return counts;
	}, [lines, showScope]);

	// Each line keeps a client-side identity that is independent of what is typed into it, so removing
	// one never hands its controls (or the focus in them) to the line below.
	const nextKey = useRef(0);
	const lineKeys = useRef<string[]>([]);
	while (lineKeys.current.length < lines.length) lineKeys.current.push(`line-${nextKey.current++}`);
	lineKeys.current.length = lines.length;

	function addLine() {
		// Pick the preferred duration when no line of the new line's scope uses it, else the first unused
		// one, falling back to the first option value
		const scope = showScope ? defaultScope : undefined;
		const duration = pickNewLineDuration(options, (value) => usedPeriods.has(budgetPeriodKey(value, scope)), defaultDuration);
		onChange([
			...lines,
			{
				max_limit: undefined,
				reset_duration: duration,
				...(scope && { scope }),
			},
		]);
	}

	function removeLine(index: number) {
		lineKeys.current.splice(index, 1);
		onChange(lines.filter((_, i) => i !== index));
	}

	function updateMaxLimit(index: number, value: number | undefined) {
		const updated = [...lines];
		updated[index] = { ...updated[index], max_limit: value };
		onChange(updated);
	}

	function updateResetDuration(index: number, value: string) {
		const updated = [...lines];
		// Drop the quarter definition when leaving a quarterly window. The API
		// rejects reset_config on any other duration, and keeping it would silently
		// resurrect a start month the operator never re-chose on switching back.
		const { reset_config, ...rest } = updated[index];
		updated[index] = isQuarterly(value) ? { ...rest, reset_config, reset_duration: value } : { ...rest, reset_duration: value };
		onChange(updated);
	}

	function updateScope(index: number, scope: LimitScope) {
		const updated = [...lines];
		updated[index] = { ...updated[index], scope };
		onChange(updated);
	}

	function updateQuarterStartMonth(index: number, quarterStartMonth: number) {
		const updated = [...lines];
		updated[index] = { ...updated[index], reset_config: { quarter_start_month: quarterStartMonth } };
		onChange(updated);
	}

	return (
		<div className="space-y-3" data-testid={testId}>
			<div className="flex items-center justify-between">
				<Label className="text-sm font-medium">{label}</Label>
				<div className="flex items-center gap-2">
					{onReset && (showReset ?? true) && (
						<Button data-testid={`${testId}-reset-btn`} type="button" variant="ghost" size="sm" onClick={onReset}>
							<RotateCcw className="mr-1 h-3 w-3" />
							{t("sheet.reset")}
						</Button>
					)}
					{(maxLines === undefined || lines.length < maxLines) && (
						<Button data-testid={`${testId}-add-btn`} variant="outline" size="sm" type="button" onClick={addLine}>
							<Plus className="mr-1 h-3 w-3" />
							{lineCopy.addLabel}
						</Button>
					)}
				</div>
			</div>

			{lines.length === 0 && (
				<div className="text-muted-foreground rounded-md border border-dashed p-3 text-center text-sm">{lineCopy.emptyLabel}</div>
			)}

			{lines.map((line, index) => {
				const isDuplicate = (usedPeriods.get(periodKeyOf(line)) || 0) > 1;
				const lineAmountError = amountError?.(line.max_limit);
				return (
					<div key={lineKeys.current[index]} className="space-y-1" data-testid={`${testId}-line-${index}`}>
						<div className={showScope ? "flex flex-col gap-2 sm:flex-row sm:items-end" : "flex items-end gap-2"}>
							<div className="flex-1">
								<NumberAndSelect
									id={`${testId}-${index}`}
									dataTestId={`${testId}-amount-${index}`}
									labelClassName="font-normal"
									label={lineCopy.amountLabel}
									value={line.max_limit}
									selectValue={line.reset_duration}
									onChangeNumber={(value) => updateMaxLimit(index, value)}
									onChangeSelect={(value) => updateResetDuration(index, value)}
									options={options}
									integer={integer}
								/>
							</div>
							{showScope && (
								<BudgetScopeSelect
									id={`${testId}-scope-${index}`}
									data-testid={`${testId}-scope-${index}`}
									labelClassName="font-normal"
									value={line.scope}
									onChange={(scope) => updateScope(index, scope)}
									options={scopeOptions}
								/>
							)}
							<Button
								data-testid={`${testId}-remove-${index}`}
								aria-label={lineCopy.removeAriaLabel(index + 1)}
								variant="ghost"
								size="icon"
								type="button"
								className="text-muted-foreground mb-0.5 h-8 w-8 shrink-0 hover:text-red-400"
								onClick={() => removeLine(index)}
							>
								<Trash2 className="h-4 w-4" />
							</Button>
						</div>
						{isQuarterly(line.reset_duration) && (
							// mr-10 (40px) = the remove button (w-8) + its gap-2, so the fiscal-year
							// row shares its right edge with the Reset Period select above it; with the
							// scope column (w-40 + gap-2) in between, that edge moves to mr-52 (208px). Below the sm
							// breakpoint the scope column stacks under the amount, so no offset is needed.
							<div className={showScope ? "sm:mr-52" : "mr-10"}>
								<QuarterStartSelect
									data-testid={`${testId}-quarter-config-${index}`}
									value={line.reset_config?.quarter_start_month}
									onChange={(month) => updateQuarterStartMonth(index, month)}
								/>
							</div>
						)}
						{lineAmountError && <p className="text-destructive pl-0.5 text-xs">{lineAmountError}</p>}
						{isDuplicate && (
							<p className="text-destructive pl-0.5 text-xs">{showScope ? lineCopy.duplicateScopeLabel : lineCopy.duplicateLabel}</p>
						)}
					</div>
				);
			})}
		</div>
	);
}
type RequestLimitLinesProps = Omit<MultiBudgetLinesProps, "options" | "copy" | "integer" | "amountError" | "defaultDuration">;

// Request limit lines: the budget line editor with request wording, whole-number maximums and the
// rate-limit reset periods (no quarterly window, which request limits do not support).
export function RequestLimitLines({ label, ...props }: RequestLimitLinesProps) {
	const { t } = useTranslation("governance");
	const { t: tCommon } = useTranslation("common");
	return (
		<MultiBudgetLines
			{...props}
			label={label ?? t("requestLimits.title")}
			options={resetDurationOptions}
			defaultDuration="1h"
			copy={{
				amountLabel: t("sheet.maximumRequests"),
				addLabel: t("requestLimits.add"),
				emptyLabel: t("requestLimits.empty"),
				removeAriaLabel: (number) => t("requestLimits.removeAria", { number }),
				duplicateLabel: t("requestLimits.duplicate"),
				duplicateScopeLabel: t("requestLimits.duplicateScope"),
			}}
			integer
			amountError={(value) => (value !== undefined && value < 1 ? tCommon("validation.valueMustBeAPositiveWholeNumber") : undefined)}
		/>
	);
}