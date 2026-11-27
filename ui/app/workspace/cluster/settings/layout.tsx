import { createFileRoute } from "@tanstack/react-router";
import ClusterSettingsPage from "./page";

export const Route = createFileRoute("/workspace/cluster/settings")({
	component: ClusterSettingsPage,
});