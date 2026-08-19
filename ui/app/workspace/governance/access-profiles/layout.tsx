import { createFileRoute } from "@tanstack/react-router";
import AccessProfilesPage from "./page";

export const Route = createFileRoute("/workspace/governance/access-profiles")({
	component: AccessProfilesPage,
});