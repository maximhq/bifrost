import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/observability/page";

export const Route = createFileRoute("/workspace/config/observability/")({
	component: Page,
});
