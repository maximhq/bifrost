import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Building } from "lucide-react";

const TEAMS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#teams";

interface TeamsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function TeamsEmptyState({ onAddClick, canCreate = true }: TeamsEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Building}
			title="Teams organize users with shared budgets and access"
			description="Create teams to group users, assign customer accounts, and set budgets and rate limits at the team level."
			readmeLink={TEAMS_DOCS_URL}
			readMoreAriaLabel="Read more about teams (opens in new tab)"
			readMoreTestId="team-button-read-more"
			actions={
				<Button aria-label="Add your first team" onClick={onAddClick} disabled={!canCreate} data-testid="team-button-add">
					Add Team
				</Button>
			}
		/>
	);
}
