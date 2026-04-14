import { createFileRoute } from "@tanstack/react-router";
import CompatibilityPage from "./page";

export const Route = createFileRoute("/workspace/config/compatibility")({
	component: CompatibilityPage,
});