import { createFileRoute } from "@tanstack/react-router";
import GuardrailsProvidersPage from "./page";

export const Route = createFileRoute("/workspace/guardrails/providers")({
	component: GuardrailsProvidersPage,
});