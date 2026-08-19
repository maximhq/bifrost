import { createFileRoute } from "@tanstack/react-router";
import DocsPage from "./page";

export const Route = createFileRoute("/workspace/docs")({
	component: DocsPage,
});