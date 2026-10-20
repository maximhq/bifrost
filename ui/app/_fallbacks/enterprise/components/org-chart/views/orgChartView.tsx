import { Network } from "lucide-react";
import ContactUsView from "../../views/contactUsView";

export function OrgChartView() {
	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="org-chart"
				icon={<Network className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock the org chart"
				description="See budgets, spend, models and tools across business units, teams and users in one tree. This feature is part of the Bifrost enterprise license."
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}