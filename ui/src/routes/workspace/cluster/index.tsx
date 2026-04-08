import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/cluster/page";

export const Route = createFileRoute("/workspace/cluster/")({
	component: Page,
});
