import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { FolderGit } from "lucide-react";
import { usePromptContext } from "../context";

export function EmptyState() {
	const { setPromptSheet, canCreate } = usePromptContext();

	return (
		<div className="text-muted-foreground flex h-full items-center justify-center">
			<div className="text-center">
				<p className="text-lg font-medium">No prompt selected</p>
				<p className="text-sm">
					{canCreate ? (
						<>
							Select a prompt from the sidebar or{" "}
							<Button
								variant="link"
								className="h-auto p-0 text-sm"
								data-testid="empty-state-create-prompt-link"
								onClick={() => setPromptSheet({ open: true })}
							>
								create a new one
							</Button>
						</>
					) : (
						"Select a prompt from the sidebar"
					)}
				</p>
			</div>
		</div>
	);
}

export function PromptsEmptyState() {
	const { setPromptSheet, canCreate } = usePromptContext();

	return (
		<EmptyStateView
			icon={FolderGit}
			title="Build, test, and version your prompts"
			description={
				canCreate
					? "Create prompts, test them with different models and parameters in the playground, and version your changes for deployment."
					: "View prompts and test them with different models and parameters in the playground."
			}
			readmeLink="https://docs.getbifrost.ai/features/prompt-repository"
			readMoreAriaLabel="Read more about prompt repository (opens in new tab)"
			readMoreTestId="empty-state-read-more"
			actions={
				canCreate && (
					<Button aria-label="Add your first prompt" data-testid="empty-state-create-prompt" onClick={() => setPromptSheet({ open: true })}>
						Add Prompt
					</Button>
				)
			}
		/>
	);
}