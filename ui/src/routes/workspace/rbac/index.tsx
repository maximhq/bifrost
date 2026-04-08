import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/rbac/page";

export const Route = createFileRoute("/workspace/rbac/")({
	component: Page,
});
