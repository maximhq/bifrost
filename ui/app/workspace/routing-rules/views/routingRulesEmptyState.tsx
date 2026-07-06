import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Route } from "lucide-react";

const ROUTING_RULES_DOCS_URL = "https://docs.getbifrost.ai/providers/routing-rules";

interface RoutingRulesEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function RoutingRulesEmptyState({ onAddClick, canCreate = true }: RoutingRulesEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Route}
			testId="routing-rules-empty-state"
			title="Routing rules direct requests using CEL conditions"
			description="Create CEL-based rules to route requests by model, provider, budget, or custom attributes. Control which provider or model handles each request."
			readmeLink={ROUTING_RULES_DOCS_URL}
			readMoreAriaLabel="Read more about routing rules (opens in new tab)"
			readMoreTestId="routing-rules-empty-read-more"
			actions={
				<Button aria-label="Add your first routing rule" data-testid="create-routing-rule-btn" onClick={onAddClick} disabled={!canCreate}>
					Add Routing Rule
				</Button>
			}
		/>
	);
}
