import { createFileRoute } from "@tanstack/react-router";
import ClusterOverviewPage from "./page";

export const Route = createFileRoute("/workspace/cluster/overview")({
	component: ClusterOverviewPage,
});