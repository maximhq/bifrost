import Page from "@/app/login/page";
import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/login/")({
	component: Page,
});
