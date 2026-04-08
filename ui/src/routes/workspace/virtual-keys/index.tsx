import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/workspace/virtual-keys/page";

export const Route = createFileRoute("/workspace/virtual-keys/")({
	component: Page,
});
