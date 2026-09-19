import type { AggregationInfo } from "@/lib/types/logs";

/** AggregationNotice explains the time resolution and coverage of archived results. */
export function AggregationNotice({ info }: { info?: AggregationInfo }) {
	if (!info) return null;
	return (
		<p className="text-muted-foreground mb-3 text-sm" data-testid="dashboard-aggregation-notice">
			{info.coverage === "retained_only"
				? "These filters can only show retained request logs. Archived history is not included."
				: "Historical results include complete hourly totals, including hours that overlap the selected range."}
		</p>
	);
}