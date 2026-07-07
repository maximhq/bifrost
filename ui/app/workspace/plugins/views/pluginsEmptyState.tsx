import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Puzzle } from "lucide-react";

const CUSTOM_PLUGINS_DOCS_URL = "https://docs.getbifrost.ai/plugins";

interface PluginsEmptyStateProps {
	onCreateClick: () => void;
	canCreate?: boolean;
}

export function PluginsEmptyState({ onCreateClick, canCreate = true }: PluginsEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Puzzle}
			testId="plugins-empty-state"
			title="Custom plugins extend Bifrost with your own business logic"
			description="Build and deploy plugins for custom integrations, workflow automation, and AI governance."
			readmeLink={CUSTOM_PLUGINS_DOCS_URL}
			readMoreAriaLabel="Read more about custom plugins (opens in new tab)"
			readMoreTestId="plugins-button-read-more"
			actions={
				<Button aria-label="Add your first plugin" data-testid="plugins-button-install-new" onClick={onCreateClick} disabled={!canCreate}>
					Add Plugin
				</Button>
			}
		/>
	);
}
