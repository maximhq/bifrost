import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/providers/model-limits/page";

export const Route = createFileRoute("/workspace/providers/model-limits/")({
	component: Page,
});
