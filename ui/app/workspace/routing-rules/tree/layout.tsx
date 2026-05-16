import { createFileRoute } from "@tanstack/react-router";
import RoutingTreePage from "./page";

export const Route = createFileRoute("/workspace/routing-rules/tree")({
	component: RoutingTreePage,
});