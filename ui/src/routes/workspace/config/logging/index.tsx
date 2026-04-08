import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/logging/page";

export const Route = createFileRoute("/workspace/config/logging/")({
	component: Page,
});
