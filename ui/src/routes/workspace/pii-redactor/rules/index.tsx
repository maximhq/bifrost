import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/pii-redactor/rules/page";

export const Route = createFileRoute("/workspace/pii-redactor/rules/")({
	component: Page,
});
