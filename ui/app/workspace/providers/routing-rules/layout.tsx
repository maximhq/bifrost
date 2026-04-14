import { createFileRoute } from "@tanstack/react-router";
import ProvidersRoutingRulesPage from "./page";

export const Route = createFileRoute("/workspace/providers/routing-rules")({
	component: ProvidersRoutingRulesPage,
});