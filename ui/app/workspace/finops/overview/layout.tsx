import { createFileRoute } from "@tanstack/react-router";
import FinOpsOverviewPage from "./page";

export const Route = createFileRoute("/workspace/finops/overview")({
	component: FinOpsOverviewPage,
});