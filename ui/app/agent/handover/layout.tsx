import { createFileRoute } from "@tanstack/react-router";

import AgentHandoverPage from "./page";

// Public landing page shown after Bifrost Agent browser sign-in completes.
export const Route = createFileRoute("/agent/handover")({
	component: AgentHandoverPage,
});