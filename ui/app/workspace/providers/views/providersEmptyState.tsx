import { EmptyStateView } from "@/components/emptyStateView";
import { Server } from "lucide-react";

const PROVIDERS_DOCS_URL = "https://docs.getbifrost.ai/providers/supported-providers/overview";

interface ProvidersEmptyStateProps {
	/** Dropdown (or button) for adding a provider; never greyed out */
	addProviderDropdown: React.ReactNode;
}

export function ProvidersEmptyState({ addProviderDropdown }: ProvidersEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Server}
			title="Add a provider to start routing requests"
			description="Configure API keys for OpenAI, Anthropic, Bedrock, and other supported providers. Bifrost unifies them behind a single API."
			readmeLink={PROVIDERS_DOCS_URL}
			readMoreAriaLabel="Read more about providers (opens in new tab)"
			readMoreTestId="providers-button-read-more"
			actions={addProviderDropdown}
		/>
	);
}
