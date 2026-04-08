import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/dashboard/page";

export const Route = createFileRoute("/workspace/dashboard/")({
	component: Page,
});
