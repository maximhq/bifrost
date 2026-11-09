import { createFileRoute } from "@tanstack/react-router";
import FinOpsExplorerPage from "./page";

export const Route = createFileRoute("/workspace/finops/explorer")({
	component: FinOpsExplorerPage,
});