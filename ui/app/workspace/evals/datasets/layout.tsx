import { createFileRoute } from "@tanstack/react-router";
import DatasetsPage from "./page";

export const Route = createFileRoute("/workspace/evals/datasets")({
	component: DatasetsPage,
});