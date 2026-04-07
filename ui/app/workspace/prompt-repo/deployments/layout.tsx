import { createFileRoute } from "@tanstack/react-router";
import PromptDeploymentsPage from "./page";

export const Route = createFileRoute("/workspace/prompt-repo/deployments")({
	component: PromptDeploymentsPage,
});
