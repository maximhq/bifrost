import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/customers/page";

export const Route = createFileRoute("/workspace/governance/customers/")({
	component: Page,
});
