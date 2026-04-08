import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/logs/mcp-logs/page";

export const Route = createFileRoute("/workspace/logs/mcp-logs/")({
	component: Page,
});
