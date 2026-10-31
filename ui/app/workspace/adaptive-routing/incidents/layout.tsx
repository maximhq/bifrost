import { createFileRoute } from "@tanstack/react-router";
import AdaptiveRoutingIncidentsPage from "./page";

export const Route = createFileRoute("/workspace/adaptive-routing/incidents")({
	component: AdaptiveRoutingIncidentsPage,
});