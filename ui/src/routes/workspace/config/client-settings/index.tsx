import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/client-settings/page";

export const Route = createFileRoute("/workspace/config/client-settings/")({
	component: Page,
});
