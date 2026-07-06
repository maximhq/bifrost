import { TeamsView } from "@enterprise/components/user-groups/teamsView";

export default function GovernanceTeamsPage() {
	return (
		<div className="flex min-h-0 w-full grow flex-col" data-testid="teams-view">
			<TeamsView />
		</div>
	);
}