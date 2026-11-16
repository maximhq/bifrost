import { createFileRoute } from "@tanstack/react-router";
import PushConfigsPage from "./page";

export const Route = createFileRoute("/workspace/agent-gateway/push-configs")({
	component: PushConfigsPage,
});