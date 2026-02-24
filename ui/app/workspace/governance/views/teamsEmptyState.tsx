"use client";

import { Button } from "@/components/ui/button";
import { Building } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const TEAMS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#teams";

interface TeamsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function TeamsEmptyState({ onAddClick, canCreate = true }: TeamsEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Building className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Teams organize users with shared budgets and access</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Create teams to group users, assign customer accounts, and set budgets and rate limits at the team level.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about teams (opens in new tab)"
						data-testid="team-button-read-more"
						onClick={() => {
							window.open(`${TEAMS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Add your first team"
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="team-button-add"
					>
						Add Team
					</Button>
				</div>
			</div>
		</div>
	);
}
