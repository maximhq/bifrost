import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/rbac/page";

export const Route = createFileRoute("/workspace/governance/rbac/")({
	component: Page,
});
