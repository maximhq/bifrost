import { createFileRoute } from "@tanstack/react-router";
import ObservabilityPage from "./page";

export const Route = createFileRoute("/workspace/config/observability")({
	component: ObservabilityPage,
});