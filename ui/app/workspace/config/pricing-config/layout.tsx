import { createFileRoute } from "@tanstack/react-router";
import PricingConfigPage from "./page";

export const Route = createFileRoute("/workspace/config/pricing-config")({
	component: PricingConfigPage,
});