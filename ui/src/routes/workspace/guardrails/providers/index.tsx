import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/guardrails/providers/page";

export const Route = createFileRoute("/workspace/guardrails/providers/")({
	component: Page,
});
