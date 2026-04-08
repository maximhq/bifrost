import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/plugins/page";

export const Route = createFileRoute("/workspace/plugins/")({
	component: Page,
});
