import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Wallet } from "lucide-react";

const MODEL_LIMITS_DOCS_URL = "https://docs.getbifrost.ai/features/governance";

interface ModelLimitsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function ModelLimitsEmptyState({ onAddClick, canCreate = true }: ModelLimitsEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Wallet}
			title="Budgets and rate limits at the model level"
			description="Set spending caps and rate limits per model. For provider-specific limits, configure each provider in Model providers."
			readmeLink={MODEL_LIMITS_DOCS_URL}
			readMoreAriaLabel="Read more about budgets and limits (opens in new tab)"
			readMoreTestId="model-limits-button-read-more"
			actions={
				<Button aria-label="Add your first model limit" onClick={onAddClick} disabled={!canCreate} data-testid="model-limits-button-create">
					Add Model Limit
				</Button>
			}
		/>
	);
}
