import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { SlidersHorizontal } from "lucide-react";

const PRICING_OVERRIDES_DOCS_URL = "https://docs.getbifrost.ai/features/governance/custom-pricing";

interface PricingOverridesEmptyStateProps {
	onCreateClick: () => void;
}

export function PricingOverridesEmptyState({ onCreateClick }: PricingOverridesEmptyStateProps) {
	return (
		<EmptyStateView
			icon={SlidersHorizontal}
			testId="pricing-overrides-empty-state"
			title="Pricing overrides customize cost tracking per scope"
			description="Define custom per-token prices for specific providers, keys, or virtual keys to accurately reflect your negotiated rates."
			readmeLink={PRICING_OVERRIDES_DOCS_URL}
			readMoreAriaLabel="Read more about pricing overrides (opens in new tab)"
			readMoreTestId="pricing-overrides-button-read-more"
			actions={
				<Button aria-label="Add your first pricing override" data-testid="pricing-override-create-btn" onClick={onCreateClick}>
					Add Override
				</Button>
			}
		/>
	);
}
