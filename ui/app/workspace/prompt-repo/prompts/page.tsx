"use client";

import { PromptProvider } from "@/components/prompts/context";
import PromptsView from "@/components/prompts/promptsView";

export default function PromptsPage() {
	return (
		<PromptProvider>
			<PromptsView />
		</PromptProvider>
	);
}
