import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { WalletCards } from "lucide-react";

const CUSTOMERS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#customers";

interface CustomersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function CustomersEmptyState({ onAddClick, canCreate = true }: CustomersEmptyStateProps) {
	return (
		<EmptyStateView
			icon={WalletCards}
			title="Customers have their own teams, budgets, and access controls"
			description="Create customer accounts to manage multi-tenant usage, assign teams, and set spending and rate limits per customer."
			readmeLink={CUSTOMERS_DOCS_URL}
			readMoreAriaLabel="Read more about customers (opens in new tab)"
			readMoreTestId="customer-button-read-more"
			actions={
				<Button aria-label="Add your first customer" onClick={onAddClick} disabled={!canCreate} data-testid="customer-button-create">
					Add Customer
				</Button>
			}
		/>
	);
}
