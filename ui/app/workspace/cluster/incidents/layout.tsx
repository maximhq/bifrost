import { createFileRoute } from "@tanstack/react-router";
import ClusterIncidentsPage from "./page";

export const Route = createFileRoute("/workspace/cluster/incidents")({
	component: ClusterIncidentsPage,
});