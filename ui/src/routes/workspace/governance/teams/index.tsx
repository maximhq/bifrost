import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/teams/page";

export const Route = createFileRoute("/workspace/governance/teams/")({
	component: Page,
});
