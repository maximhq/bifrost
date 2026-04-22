import { createFileRoute } from "@tanstack/react-router";
import PerformanceTuningPage from "./page";

export const Route = createFileRoute("/workspace/config/performance-tuning")({
	component: PerformanceTuningPage,
});