import { createFileRoute } from "@tanstack/react-router";
import GovernanceBusinessUnitsPage from "./page";

export const Route = createFileRoute("/workspace/governance/business-units")({
	component: GovernanceBusinessUnitsPage,
});