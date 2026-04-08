import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/logs/connectors/page";

export const Route = createFileRoute("/workspace/logs/connectors/")({
	component: Page,
});
