import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/prompt-repo/deployments/page";

export const Route = createFileRoute("/workspace/prompt-repo/deployments/")({
	component: Page,
});
