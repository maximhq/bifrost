import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/pricing-config/page";

export const Route = createFileRoute("/workspace/config/pricing-config/")({
	component: Page,
});
