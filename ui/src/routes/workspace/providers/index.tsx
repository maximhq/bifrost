import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/providers/page";

export const Route = createFileRoute("/workspace/providers/")({
	component: Page,
});
