import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/mcp-registry/page";

export const Route = createFileRoute("/workspace/mcp-registry/")({
	component: Page,
});
