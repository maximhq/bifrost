import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/pii-redactor/providers/page";

export const Route = createFileRoute("/workspace/pii-redactor/providers/")({
	component: Page,
});
