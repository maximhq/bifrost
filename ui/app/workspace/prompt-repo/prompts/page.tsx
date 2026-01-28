"use client";

import { PromptProvider } from "@/app/enterprise/components/prompts/context";
import PromptsView from "@enterprise/components/prompts/promptsView";

export default function PromptsPage() {
	return (
		<PromptProvider>
			<PromptsView />
		</PromptProvider>
	);
}
