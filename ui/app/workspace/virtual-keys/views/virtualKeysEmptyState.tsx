import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { KeyRound } from "lucide-react";

const VIRTUAL_KEYS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys";

interface VirtualKeysEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function VirtualKeysEmptyState({ onAddClick, canCreate = true }: VirtualKeysEmptyStateProps) {
	return (
		<EmptyStateView
			icon={KeyRound}
			testId="virtual-keys-empty-state"
			title="Virtual keys control access, budgets, and rate limits"
			description="Create virtual keys to assign permissions, spending limits, and usage quotas to teams, customers, or API clients."
			readmeLink={VIRTUAL_KEYS_DOCS_URL}
			readMoreAriaLabel="Read more about virtual keys (opens in new tab)"
			readMoreTestId="virtual-keys-button-read-more"
			actions={
				<Button aria-label="Add your first virtual key" onClick={onAddClick} disabled={!canCreate} data-testid="create-vk-btn">
					Add Virtual Key
				</Button>
			}
		/>
	);
}
