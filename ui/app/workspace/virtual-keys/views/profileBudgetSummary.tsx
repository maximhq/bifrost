import { formatResetDuration } from "@/components/budgetDisplay";
import { Progress } from "@/components/ui/progress";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { formatCurrency } from "@/lib/utils/governance";
import { useTranslation } from "react-i18next";
import { profileBudgetBarClass, profileBudgetBarPercent, type ProfileBudgetSummary as Summary } from "../hooks/useVirtualKeyUsage.utils";

// One bar for a key several access profiles pay for: red only when none of them can pay.
export function ProfileBudgetSummary({
	summary,
	calendarAligned,
	sharedNote,
}: {
	summary: Summary;
	calendarAligned?: boolean;
	sharedNote?: string;
}) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const pct = profileBudgetBarPercent(summary);

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div
					tabIndex={0}
					className="focus-visible:ring-ring/50 min-w-[160px] space-y-1.5 rounded-sm outline-none focus-visible:ring-1"
					data-testid="vk-profile-budget-summary"
				>
					<div className="flex items-center justify-between gap-4">
						<span className="text-xs font-medium">
							{summary.limit > 0 ? formatCurrency(summary.limit) : t("virtualKeys.profileNoBudget")}
							<span className="text-muted-foreground ml-1 text-[10px]">
								{t("virtualKeys.profileCount", { count: summary.lines.length })}
							</span>
						</span>
						<span className="text-muted-foreground text-xs">{formatResetDuration(tc, summary.resetDuration, calendarAligned)}</span>
					</div>
					<Progress aria-label={tc("budgetDisplay.usageAria")} value={pct} className={cn("h-1.5", profileBudgetBarClass(summary))} />
				</div>
			</TooltipTrigger>
			<TooltipContent className="max-w-lg">
				<ul className="flex flex-col gap-1 font-mono text-xs">
					{summary.lines.map((l, i) => (
						<li key={`${l.name}-${i}`} className="flex justify-between gap-4">
							<span className={cn("truncate", l.spent && "text-red-400")}>{l.name}</span>
							<span className="shrink-0">
								{l.limit === undefined
									? t("virtualKeys.profileNoBudget")
									: `${formatCurrency(l.used)} / ${formatCurrency(l.limit)}${l.resetDuration ? ` · ${formatResetDuration(tc, l.resetDuration, calendarAligned)}` : ""}`}
							</span>
						</li>
					))}
				</ul>
				<p className="text-muted-foreground mt-2 text-xs">{t("virtualKeys.profilesTotalNote")}</p>
				{sharedNote ? <p className="text-muted-foreground mt-1 text-xs">{sharedNote}</p> : null}
			</TooltipContent>
		</Tooltip>
	);
}