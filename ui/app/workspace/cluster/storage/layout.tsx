import { createFileRoute } from "@tanstack/react-router";
import ClusterStoragePage from "./page";

export const Route = createFileRoute("/workspace/cluster/storage")({
	component: ClusterStoragePage,
});