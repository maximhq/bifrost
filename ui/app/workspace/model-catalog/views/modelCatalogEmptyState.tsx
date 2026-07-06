import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { LayoutGrid } from "lucide-react";

export function ModelCatalogEmptyState() {
	return (
		<EmptyStateView
			icon={LayoutGrid}
			title="No providers configured yet"
			description="Configure your first model provider to see an overview of all providers, API keys, models, and usage metrics."
			actions={
				<Button asChild data-testid="modelcatalog-configure-providers-cta">
					<Link to="/workspace/providers">Configure Providers</Link>
				</Button>
			}
		/>
	);
}
