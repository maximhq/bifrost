import { createFileRoute } from "@tanstack/react-router";
import FeatureFlagsPage from "./page";

export const Route = createFileRoute("/workspace/config/feature-flags")({
	component: FeatureFlagsPage,
});