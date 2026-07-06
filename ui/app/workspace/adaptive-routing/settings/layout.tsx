import { createFileRoute } from "@tanstack/react-router";
import AdaptiveRoutingSettingsPage from "./page";

export const Route = createFileRoute("/workspace/adaptive-routing/settings")({
	component: AdaptiveRoutingSettingsPage,
});