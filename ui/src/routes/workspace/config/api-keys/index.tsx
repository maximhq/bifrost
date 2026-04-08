import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/config/api-keys/page";

export const Route = createFileRoute("/workspace/config/api-keys/")({
	component: Page,
});
