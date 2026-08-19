import { TeamsView } from "@enterprise/components/user-groups/teamsView";

export default function GovernanceTeamsPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] w-full flex-col p-4" data-testid="teams-view">
			<TeamsView />
		</div>
	);
}