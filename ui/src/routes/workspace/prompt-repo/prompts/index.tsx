import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/prompt-repo/prompts/page";

export const Route = createFileRoute("/workspace/prompt-repo/prompts/")({
	component: Page,
});
