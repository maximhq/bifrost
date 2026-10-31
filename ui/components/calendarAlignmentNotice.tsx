import { Alert, AlertDescription } from "@/components/ui/alert";
import { isMultiUnitDuration } from "@/lib/constants/governance";

export function CalendarAlignmentNotice({ enabled, durations }: { enabled: boolean; durations: string[] }) {
	const rolling = [...new Set(durations.filter(isMultiUnitDuration))];
	if (!enabled || rolling.length === 0) return null;
	return (
		<Alert variant="warning" data-testid="calendar-alignment-rolling-warning">
			<AlertDescription>
				{rolling.join(", ")} will use rolling windows for their full duration. Calendar alignment only applies to 1d, 1w, 1M, 1Q and 1Y.
			</AlertDescription>
		</Alert>
	);
}