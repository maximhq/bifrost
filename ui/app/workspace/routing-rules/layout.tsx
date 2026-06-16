import { createFileRoute, Outlet, useChildMatches } from "@tanstack/react-router";
import RoutingRulesPage from "./page";

function RouteComponent() {
	const childMatches = useChildMatches();
	return childMatches.length === 0 ? <RoutingRulesPage /> : <Outlet />;
}

export const Route = createFileRoute("/workspace/routing-rules")({
	component: RouteComponent,
});