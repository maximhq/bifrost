import { OrgChartView } from "@enterprise/components/org-chart/views/orgChartView";

export default function GovernanceOrgChartPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full flex-col p-4">
			<OrgChartView />
		</div>
	);
}