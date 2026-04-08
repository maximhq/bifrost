import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/alert-channels/page";

export const Route = createFileRoute("/workspace/alert-channels/")({
	component: Page,
});
