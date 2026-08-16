import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { currentQuarterIndex, nextQuarterReset, quarterRanges, quarterStartMonthOptions } from "@/lib/constants/governance";
import { cn } from "@/lib/utils";
import { format } from "date-fns";

interface QuarterStartSelectProps {
	"data-testid"?: string;
	/** First month of Q1 as 1-12; undefined means January. */
	value?: number;
	onChange: (quarterStartMonth: number) => void;
}

/**
 * Advanced setting for a quarterly budget: which month opens Q1.
 *
 * Shared by every budget editor rather than inlined. The fiscal-year row and the
 * quarter map share one bordered box so they read as a single grouped setting. The
 * month select keeps the Reset Period select's size (h-9 / w-40), and the
 * quarter map below makes the setting legible: quarter boundaries repeat every three
 * months, so April/July/October fiscal years reset on the same days as January - the
 * only visible difference is which range is labelled Q1 and which quarter is current.
 * The helper line spells out the concrete next reset date so the effect is unambiguous.
 */
export default function QuarterStartSelect({ "data-testid": testId, value, onChange }: QuarterStartSelectProps) {
	const quarters = quarterRanges(value);
	const current = currentQuarterIndex(value);
	const nextReset = nextQuarterReset(value);
	// The quarter ends the day before the next reset boundary.
	const currentQuarterEnd = new Date(nextReset.getTime() - 86_400_000);

	return (
		<div className="bg-muted/20 space-y-2.5 rounded-sm border p-3" data-testid={testId}>
			{/* Stacks below sm so the label and month select don't crush each other on
			    narrow viewports; horizontal (select right-aligned inside the box) above. */}
			<div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3">
				<Label className="font-normal">Fiscal year starts</Label>
				<Select value={String(value ?? 1)} onValueChange={(month) => onChange(Number(month))}>
					<SelectTrigger
						aria-label="Fiscal year starts"
						className="h-9 w-full shrink-0 sm:w-40"
						data-testid={testId ? `${testId}-trigger` : undefined}
					>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{quarterStartMonthOptions.map((month) => (
							<SelectItem key={month.value} value={month.value} data-testid={testId ? `${testId}-option-${month.value}` : undefined}>
								{month.label}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>

			<div className="grid grid-cols-4 gap-1.5">
				{quarters.map((quarter, index) => {
					const isCurrent = index === current;
					return (
						<div
							key={quarter.label}
							className={cn("flex flex-col gap-[3px] rounded-sm border p-2", isCurrent && "border-ring bg-secondary")}
							data-testid={isCurrent && testId ? `${testId}-current-quarter` : undefined}
						>
							<div className="flex items-center justify-between">
								<span className="text-muted-foreground font-mono text-[10.5px]">{quarter.label}</span>
								{isCurrent && <span className="bg-primary h-[5px] w-[5px] rounded-full" />}
							</div>
							<span className={cn("font-mono text-xs", isCurrent ? "text-foreground" : "text-muted-foreground")}>{quarter.range}</span>
						</div>
					);
				})}
			</div>
			<p className="text-muted-foreground border-t pt-2.5 text-[0.8rem] leading-relaxed">
				Next reset on <span className="text-foreground font-medium">{format(nextReset, "MMM d, yyyy")}</span>. The current quarter ends{" "}
				{format(currentQuarterEnd, "MMM d, yyyy")}.
			</p>
		</div>
	);
}
