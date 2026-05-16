import { createFileRoute } from "@tanstack/react-router";
import ModelLimitsPage from "./page";

function RouteComponent() {
	return <ModelLimitsPage />;
}

export const Route = createFileRoute("/workspace/model-limits")({
	component: RouteComponent,
});