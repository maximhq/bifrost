import { createFileRoute } from "@tanstack/react-router";
import ProvidersModelLimitsPage from "./page";

export const Route = createFileRoute("/workspace/providers/model-limits")({
	component: ProvidersModelLimitsPage,
});