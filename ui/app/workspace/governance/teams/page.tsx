import { TeamsView } from "@enterprise/components/user-groups/teamsView";

export default function GovernanceTeamsPage() {
	return (<div className="no-padding-parent mx-auto h-[calc(100dvh-1rem)] w-full p-4 flex flex-col" data-testid="teams-view">
		<TeamsView />
	</div>);
}