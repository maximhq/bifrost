import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/logs/page";

export const Route = createFileRoute("/workspace/logs/")({
	component: Page,
});
