import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import EvaluatorsPage from "./page";

function RouteComponent() {
	const childMatches = useChildMatches();
	return childMatches.length === 0 ? <EvaluatorsPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/evals/evaluators")({
	component: RouteComponent,
});