import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/logs/dashboard/page";

export const Route = createFileRoute("/workspace/logs/dashboard/")({
	component: Page,
});
