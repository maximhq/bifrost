import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { TIME_PERIODS } from "@/lib/utils/timeRange";
import type { TrafficFilter } from "./useRouterTraffic";

interface TrafficPeriodPickerProps {
	filter: TrafficFilter;
	testId: string;
}

// TrafficPeriodPicker chooses the period the routers' traffic figures cover.
// It is the date picker the dashboard and logs pages use, with their presets
// and their timezone choice, so a period reads the same everywhere.
export function TrafficPeriodPicker({ filter, testId }: TrafficPeriodPickerProps) {
	return (
		<DateTimePickerWithRange
			dateTime={filter.dateRange}
			onDateTimeUpdate={filter.setDateRange}
			preDefinedPeriods={TIME_PERIODS}
			predefinedPeriod={filter.selection.period}
			onPredefinedPeriodChange={filter.setPeriod}
			triggerTestId={testId}
			buttonClassName="h-9"
			popupAlignment="end"
			showTimezone
			timezone={filter.timezone}
			onTimezoneChange={filter.setTimezone}
		/>
	);
}