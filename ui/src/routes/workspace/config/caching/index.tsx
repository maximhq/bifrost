import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/caching/page";

export const Route = createFileRoute("/workspace/config/caching/")({
	component: Page,
});
