import { createFileRoute } from "@tanstack/react-router";
import GuardrailsConfigurationPage from "./page";

export const Route = createFileRoute("/workspace/guardrails/configuration")({
	component: GuardrailsConfigurationPage,
});