import { createFileRoute } from "@tanstack/react-router";
import CanaryPage from "./page";

export const Route = createFileRoute("/workspace/config/canary")({
	component: CanaryPage,
});