import { BusinessUnitsView } from "@enterprise/components/user-groups/businessUnitsView";

export default function GovernanceBusinessUnitsPage() {
	return (
		<div className="no-padding-parent mx-auto h-[calc(100dvh-1rem)] w-full p-4 flex flex-col">
			<BusinessUnitsView />
		</div>
	);
}