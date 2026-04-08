import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/page";

export const Route = createFileRoute("/workspace/governance/")({
	component: Page,
});
