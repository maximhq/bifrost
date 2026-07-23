import { Progress } from "@/components/ui/progress";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationLabels, supportsCalendarAlignment } from "@/lib/constants/governance";
import { Budget } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { formatCurrency, getEffectiveBudgetLimit, hasActiveBudgetOverride } from "@/lib/utils/governance";

interface BudgetDisplayProps {
	budgets: Budget[] | null | undefined;
	/** When true, alignable durations (day/week/month/year) get a "(calendar)" suffix. */
	calendarAligned?: boolean;
}

const formatResetDuration = (duration?: string | null, calendarAligned?: boolean) => {
	if (!duration) return "";
	const label = resetDurationLabels[duration] || duration;
	return calendarAligned && supportsCalendarAlignment(duration) ? `${label} (calendar)` : label;
};

/**
 * Renders a team-style usage bar per budget line: max limit + reset period on top, a
 * color-coded progress bar (emerald < 80% < amber < exhausted = red), and a tooltip with
 * the exact current/max spend. Mirrors RateLimitDisplay for visual consistency across tables.
 */
export function BudgetDisplay({ budgets, calendarAligned }: BudgetDisplayProps) {
	if (!budgets || budgets.length === 0) {
		return <span className="text-muted-foreground text-sm">-</span>;
	}

	return (
		<div className="min-w-[160px] space-y-2.5">
			{budgets.map((b, idx) => {
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
									<span className="font-medium">
										{formatCurrency(effectiveMaxLimit)}
										{hasOverride ? <span className="text-muted-foreground ml-1 text-[10px]">override</span> : null}
									</span>
									<span className="text-muted-foreground text-xs">{formatResetDuration(b.reset_duration, calendarAligned)}</span>
								</div>
								<Progress value={pct} className={cn("bg-muted/70 dark:bg-muted/30 h-1.5", barClass)} />
							</div>
						</TooltipTrigger>
						<TooltipContent>
							<p className="font-medium">
								{formatCurrency(b.current_usage)} / {formatCurrency(effectiveMaxLimit)}
							</p>
							{hasOverride ? (
								<p className="text-primary-foreground/80 text-xs">
									Base {formatCurrency(b.max_limit)} + {formatCurrency(b.override_amount ?? 0)} override
								</p>
							) : null}
							{b.reset_duration ? (
								<p className="text-primary-foreground/80 text-xs">Resets {formatResetDuration(b.reset_duration, calendarAligned)}</p>
							) : null}
						</TooltipContent>
					</Tooltip>
				);
			})}
		</div>
	);
}