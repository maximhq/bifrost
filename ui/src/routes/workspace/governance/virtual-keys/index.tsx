import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/governance/virtual-keys/page";

export const Route = createFileRoute("/workspace/governance/virtual-keys/")({
	component: Page,
});
