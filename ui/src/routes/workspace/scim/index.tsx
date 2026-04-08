import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/scim/page";

export const Route = createFileRoute("/workspace/scim/")({
	component: Page,
});
