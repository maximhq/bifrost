import { createFileRoute } from "@tanstack/react-router";
import GovernanceCustomersPage from "./page";

export const Route = createFileRoute("/workspace/governance/customers")({
	component: GovernanceCustomersPage,
});