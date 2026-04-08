import { createFileRoute } from "@tanstack/react-router";
import Page from "@/app/pprof/page";

export const Route = createFileRoute("/pprof/")({
	component: Page,
});
