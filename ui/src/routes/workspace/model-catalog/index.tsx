import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/model-catalog/page";

export const Route = createFileRoute("/workspace/model-catalog/")({
	component: Page,
});
