import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/audit-logs/page";

export const Route = createFileRoute("/workspace/audit-logs/")({
	component: Page,
});
