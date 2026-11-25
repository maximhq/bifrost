import { createFileRoute } from "@tanstack/react-router";
import FinOpsForecastPage from "./page";

export const Route = createFileRoute("/workspace/finops/forecast")({
	component: FinOpsForecastPage,
});