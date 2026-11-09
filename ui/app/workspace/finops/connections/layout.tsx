import { createFileRoute } from "@tanstack/react-router";
import FinOpsConnectionsPage from "./page";

export const Route = createFileRoute("/workspace/finops/connections")({
	component: FinOpsConnectionsPage,
});