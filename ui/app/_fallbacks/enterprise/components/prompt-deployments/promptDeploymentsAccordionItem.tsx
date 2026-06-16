"use client";

import { usePromptContext } from "@/components/prompts/context";
import { AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { cn } from "@/lib/utils";
import PromptDeploymentView from "./promptDeploymentView";

export type SettingsSidebarSection = "parameters" | "deployments";

export function PromptDeploymentsAccordionItem({
	activeSection,
}: {
	activeSection: SettingsSidebarSection | undefined;
}) {
	const { selectedPromptId } = usePromptContext();
	if (!selectedPromptId) {
		return null;
	}

	const deploymentsOpen = activeSection === "deployments";

	return (
		<AccordionItem
			value="deployments"
			className={cn(
				"border-border/60 flex min-h-0 flex-col border-b-0 border-t pt-1",
				deploymentsOpen ? "min-h-0 grow overflow-hidden" : "shrink-0 grow-0",
			)}
		>
			<AccordionTrigger data-testid="prompt-deployments-trigger" className="text-muted-foreground w-full min-w-0 shrink-0 py-3 pr-1 text-xs font-medium uppercase hover:no-underline [&[data-state=open]>svg]:rotate-180">
				<span className="min-w-0 flex-1 text-left font-semibold">Deployments</span>
			</AccordionTrigger>
			<AccordionContent
				containerClassName="data-[state=open]:flex data-[state=open]:min-h-0 data-[state=open]:flex-1 data-[state=open]:flex-col"
				className="min-h-0 flex-1 overflow-y-auto pb-2 pt-0"
			>
				<PromptDeploymentView omitTitle />
			</AccordionContent>
		</AccordionItem>
	);
}
