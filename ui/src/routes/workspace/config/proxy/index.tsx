import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/proxy/page";

export const Route = createFileRoute("/workspace/config/proxy/")({
	component: Page,
});
