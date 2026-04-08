import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/mcp-gateway/page";

export const Route = createFileRoute("/workspace/config/mcp-gateway/")({
	component: Page,
});
