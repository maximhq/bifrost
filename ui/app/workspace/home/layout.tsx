import { createFileRoute } from "@tanstack/react-router";
import HomePage from "./page";

export const Route = createFileRoute("/workspace/home")({
	component: HomePage,
});