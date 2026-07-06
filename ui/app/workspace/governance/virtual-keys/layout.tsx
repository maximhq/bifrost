import { createFileRoute } from "@tanstack/react-router";
import GovernanceVirtualKeysPage from "./page";

export const Route = createFileRoute("/workspace/governance/virtual-keys")({
	component: GovernanceVirtualKeysPage,
});