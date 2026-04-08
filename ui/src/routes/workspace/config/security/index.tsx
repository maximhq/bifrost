import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/security/page";

export const Route = createFileRoute("/workspace/config/security/")({
	component: Page,
});
