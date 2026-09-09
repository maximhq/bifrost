import { createFileRoute } from "@tanstack/react-router";
import ProjectsPage from "./page";

export const Route = createFileRoute("/workspace/governance/projects")({
	component: ProjectsPage,
});