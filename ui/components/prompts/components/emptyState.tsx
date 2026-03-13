import { Button } from "@/components/ui/button";
import { ArrowUpRight, SquareTerminal } from "lucide-react";
import { usePromptContext } from "../context";

const PROMPTS_DOCS_URL = "https://docs.getbifrost.ai/features/prompt-repo/overview";

export function EmptyState() {
	const { setPromptSheet } = usePromptContext();

	return (
		<div className="text-muted-foreground flex h-full items-center justify-center">
			<div className="text-center">
				<p className="text-lg font-medium">No prompt selected</p>
				<p className="text-sm">
					Select a prompt from the sidebar or{" "}
					<Button variant="link" className="h-auto p-0 text-sm" data-testid="empty-state-create-prompt-link" onClick={() => setPromptSheet({ open: true })}>
						create a new one
					</Button>
				</p>
			</div>
		</div>
	);
}

export function PromptsEmptyState() {
	const { setPromptSheet } = usePromptContext();

	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<SquareTerminal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Build, test, and version your prompts</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Create prompts, test them with different models and parameters in the playground, and version your changes for deployment.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about prompt repository (opens in new tab)"
						data-testid="empty-state-read-more"
						onClick={() => {
							window.open(`${PROMPTS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Create your first prompt"
						data-testid="empty-state-create-prompt"
						onClick={() => setPromptSheet({ open: true })}
					>
						Create Prompt
					</Button>
				</div>
			</div>
		</div>
	);
}
