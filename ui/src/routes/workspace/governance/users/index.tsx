import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/users/page";

export const Route = createFileRoute("/workspace/governance/users/")({
	component: Page,
});
