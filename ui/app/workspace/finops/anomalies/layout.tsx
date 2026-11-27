import { createFileRoute } from "@tanstack/react-router";
import FinOpsAnomaliesPage from "./page";

export const Route = createFileRoute("/workspace/finops/anomalies")({
	component: FinOpsAnomaliesPage,
});