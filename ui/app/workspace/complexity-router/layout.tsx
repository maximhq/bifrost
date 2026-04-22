import { createFileRoute } from "@tanstack/react-router";
import ComplexityRouterPage from "./page";

export const Route = createFileRoute("/workspace/complexity-router")({
	component: ComplexityRouterPage,
});