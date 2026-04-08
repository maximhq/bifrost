import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/performance-tuning/page";

export const Route = createFileRoute("/workspace/config/performance-tuning/")({
	component: Page,
});
