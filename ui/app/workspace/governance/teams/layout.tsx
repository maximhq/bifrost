import { createFileRoute } from "@tanstack/react-router";
import GovernanceTeamsPage from "./page";

export const Route = createFileRoute("/workspace/governance/teams")({
	component: GovernanceTeamsPage,
});