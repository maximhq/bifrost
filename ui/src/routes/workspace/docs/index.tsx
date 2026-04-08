import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/docs/page";

export const Route = createFileRoute("/workspace/docs/")({
	component: Page,
});
