import { createFileRoute } from "@tanstack/react-router";
import GovernanceUsersPage from "./page";

export const Route = createFileRoute("/workspace/governance/users")({
	component: GovernanceUsersPage,
});