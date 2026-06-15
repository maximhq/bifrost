import { createFileRoute } from "@tanstack/react-router";
import GovernanceRbacPage from "./page";

export const Route = createFileRoute("/workspace/governance/rbac")({
	component: GovernanceRbacPage,
});