import { createFileRoute } from "@tanstack/react-router";
import EvaluatorEditorPage from "./page";

export const Route = createFileRoute("/workspace/evals/evaluators/editor")({
	component: EvaluatorEditorPage,
});