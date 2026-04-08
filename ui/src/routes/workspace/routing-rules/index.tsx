import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/routing-rules/page";

export const Route = createFileRoute("/workspace/routing-rules/")({
	component: Page,
});
