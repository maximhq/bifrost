import { createFileRoute } from "@tanstack/react-router";
import EvaluatorCatalogPage from "./page";

export const Route = createFileRoute("/workspace/evals/evaluators/catalog")({
	component: EvaluatorCatalogPage,
});