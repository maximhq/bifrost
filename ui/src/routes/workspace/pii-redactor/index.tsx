import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/pii-redactor/page";

export const Route = createFileRoute("/workspace/pii-redactor/")({
	component: Page,
});
