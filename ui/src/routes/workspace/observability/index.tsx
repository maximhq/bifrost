import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/observability/page";

export const Route = createFileRoute("/workspace/observability/")({
	component: Page,
});
