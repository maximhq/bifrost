import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { formatQuarterPreview, quarterStartMonthOptions } from "@/lib/constants/governance";

interface QuarterStartSelectProps {
	"data-testid"?: string;
	/** First month of Q1 as 1-12; undefined means January. */
	value?: number;
	onChange: (quarterStartMonth: number) => void;
}

/**
 * Advanced setting for a quarterly budget: which month opens Q1.
 *
 * Shared by every budget editor rather than inlined, because the preview below
 * the select is the part that makes the setting legible and a second copy would
 * drift. Quarter boundaries repeat every three months, so the start month only
 * changes reset dates modulo 3: January, April, July and October all reset on
 * the same days, which covers the common UK/India (April), US federal (October)
 * and Australian (July) fiscal years. For those an operator sees no change in
 * reset behaviour at all, and the preview is the only feedback that the setting
 * took effect - what differs is which quarter is labelled Q1.
 */
export default function QuarterStartSelect({ "data-testid": testId, value, onChange }: QuarterStartSelectProps) {
	return (
		<div className="bg-muted/20 mt-1 space-y-1.5 rounded-sm border p-2.5" data-testid={testId}>
			<div className="flex w-full flex-row items-center gap-2">
				<Label className="text-muted-foreground font-medium">Fiscal year starts</Label>
				<div className="ml-auto flex flex-col items-end">
					<Select value={String(value ?? 1)} onValueChange={(month) => onChange(Number(month))}>
						<SelectTrigger className="h-8 w-44" data-testid={testId ? `${testId}-trigger` : undefined}>
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
					<p className="text-muted-foreground text-[11px] italic">{formatQuarterPreview(value)}</p>
				</div>
			</div>
		</div>
	);
}