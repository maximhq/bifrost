import { Progress } from "@/components/ui/progress";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { fiscalQuarterNote, resetDurationLabels, supportsCalendarAlignment } from "@/lib/constants/governance";
import { Budget } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency, getEffectiveBudgetLimit, hasActiveBudgetOverride } from "@/lib/utils/governance";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

interface BudgetDisplayProps {
	/** A line may carry a label naming what the budget is on, for tables where lines on the same window differ by scope. */
	budgets: (Budget & { label?: string })[] | null | undefined;
	/** When true, alignable durations (day/week/month/quarter/year) get a "(calendar)" suffix. */
	calendarAligned?: boolean;
	/** Draw at most this many lines; the rest collapse into a "+x more" line whose tooltip names them. */
	maxVisible?: number;
}

const formatResetDuration = (t: TFunction<"common">, duration?: string | null, calendarAligned?: boolean) => {
	if (!duration) return "";
	const label = resetDurationLabels[duration] || duration;
	return calendarAligned && supportsCalendarAlignment(duration) ? t("calendarAlignedLabel", { label }) : label;
};

/**
 * Renders a team-style usage bar per budget line: max limit + reset period on top, a
 * color-coded progress bar (emerald < 80% < amber < exhausted = red), and a tooltip with
 * the exact current/max spend. Mirrors RateLimitDisplay for visual consistency across tables.
 */
export function BudgetDisplay({ budgets, calendarAligned, maxVisible }: BudgetDisplayProps) {
	const { t } = useTranslation("common");
	if (!budgets || budgets.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}

	const shown = maxVisible != null ? budgets.slice(0, maxVisible) : budgets;
	const hidden = maxVisible != null ? budgets.slice(maxVisible) : [];

	return (
		<div className="min-w-[160px] space-y-2.5">
			{shown.map((b, idx) => {
				const effectiveMaxLimit = getEffectiveBudgetLimit(b);
				const hasOverride = hasActiveBudgetOverride(b);
				const pct = effectiveMaxLimit > 0 ? Math.min((b.current_usage / effectiveMaxLimit) * 100, 100) : 0;
				const isExhausted = effectiveMaxLimit > 0 && b.current_usage >= effectiveMaxLimit;
				const barClass = isExhausted ? "[&>div]:bg-red-500/70" : pct > 80 ? "[&>div]:bg-amber-500/70" : "[&>div]:bg-emerald-500/70";

				return (
					<Tooltip key={b.id ?? idx}>
						<TooltipTrigger asChild>
							<div className="space-y-1.5">
								<div className="flex items-center justify-between gap-4">
									<span className="text-xs font-medium">
										{formatCurrency(effectiveMaxLimit)}
										{b.label ? <span className="text-muted-foreground ml-1 text-xs font-normal">{b.label}</span> : null}
										{hasOverride ? <span className="text-muted-foreground ml-1 text-[10px]">{t("budgetDisplay.override")}</span> : null}
									</span>
									<span className="text-muted-foreground text-xs">
										{formatResetDuration(t, b.reset_duration, calendarAligned)}
										{fiscalQuarterNote(b.reset_duration, b.reset_config)}
									</span>
								</div>
								<Progress
									aria-label={t("budgetDisplay.usageAria")}
									value={pct}
									className={cn("bg-muted/70 dark:bg-muted/30 h-1.5", barClass)}
								/>
							</div>
						</TooltipTrigger>
						<TooltipContent>
							<p className="font-medium">
								{formatCurrency(b.current_usage)} / {formatCurrency(effectiveMaxLimit)}
							</p>
							{hasOverride ? (
								<p className="text-muted-foreground mt-1 text-xs">
									{t("budgetDisplay.baseTooltip", { base: formatCurrency(b.max_limit), override: formatCurrency(b.override_amount ?? 0) })}
								</p>
							) : null}
							{b.reset_duration ? (
								<p className="text-muted-foreground mt-1 text-xs">
									{t("resetsWhen", {
										when: `${formatResetDuration(t, b.reset_duration, calendarAligned)}${fiscalQuarterNote(b.reset_duration, b.reset_config)}`,
									})}
								</p>
							) : null}
						</TooltipContent>
					</Tooltip>
				);
			})}
			{hidden.length > 0 ? (
				<Tooltip>
					<TooltipTrigger asChild>
						<button type="button" className="text-muted-foreground block cursor-help text-left text-xs">
							{t("providerList.moreCount", { count: hidden.length })}
						</button>
					</TooltipTrigger>
					<TooltipContent>
						{hidden.map((b, idx) => (
							<p key={b.id ?? idx} className="text-xs">
								{formatCurrency(b.current_usage)} / {formatCurrency(getEffectiveBudgetLimit(b))}
								{b.label ? ` · ${b.label}` : ""}
								{b.reset_duration ? ` · ${formatResetDuration(t, b.reset_duration, calendarAligned)}` : ""}
							</p>
						))}
					</TooltipContent>
				</Tooltip>
			) : null}
		</div>
	);
}