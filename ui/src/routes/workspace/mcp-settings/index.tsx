import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/mcp-settings/page";

export const Route = createFileRoute("/workspace/mcp-settings/")({
	component: Page,
});
