import { PromptProvider } from "@/components/prompts/context";
import PromptsView from "@/components/prompts/promptsView";

export default function PromptRepoPage() {
	return (
		<PromptProvider>
			<PromptsView />
		</PromptProvider>
	);
}