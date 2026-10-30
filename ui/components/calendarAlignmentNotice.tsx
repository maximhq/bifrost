import { Alert, AlertDescription } from "@/components/ui/alert";
import { useTranslation } from "react-i18next";
import { isMultiUnitDuration } from "@/lib/constants/governance";

export function CalendarAlignmentNotice({ enabled, durations }: { enabled: boolean; durations: string[] }) {
	const { t } = useTranslation("common");
	const rolling = [...new Set(durations.filter(isMultiUnitDuration))];
	if (!enabled || rolling.length === 0) return null;
	return (
		<Alert variant="warning" data-testid="calendar-alignment-rolling-warning">
			<AlertDescription>{t("calendarAlignment.rollingNotice", { durations: rolling.join(", ") })}</AlertDescription>
		</Alert>
	);
}