import { BusinessUnitsView } from "@enterprise/components/user-groups/businessUnitsView";

export default function GovernanceBusinessUnitsPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_1rem)] w-full flex-col p-4">
			<BusinessUnitsView />
		</div>
	);
}