import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/custom-pricing/page";

export const Route = createFileRoute("/workspace/custom-pricing/")({
	component: Page,
});
