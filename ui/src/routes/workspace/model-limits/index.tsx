import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/model-limits/page";

export const Route = createFileRoute("/workspace/model-limits/")({
	component: Page,
});
