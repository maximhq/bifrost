import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/mcp-auth-config/page";

export const Route = createFileRoute("/workspace/mcp-auth-config/")({
	component: Page,
});
