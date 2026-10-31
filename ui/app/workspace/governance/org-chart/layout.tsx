import { createFileRoute } from "@tanstack/react-router";
import GovernanceOrgChartPage from "./page";

export const Route = createFileRoute("/workspace/governance/org-chart")({
	component: GovernanceOrgChartPage,
});