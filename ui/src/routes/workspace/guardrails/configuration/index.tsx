import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/guardrails/configuration/page";

export const Route = createFileRoute("/workspace/guardrails/configuration/")({
	component: Page,
});
