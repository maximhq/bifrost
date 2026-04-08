import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/guardrails/page";

export const Route = createFileRoute("/workspace/guardrails/")({
	component: Page,
});
