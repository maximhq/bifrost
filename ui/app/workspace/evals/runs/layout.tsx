import { createFileRoute } from "@tanstack/react-router";
import TestRunsPage from "./page";

export const Route = createFileRoute("/workspace/evals/runs")({
	component: TestRunsPage,
});