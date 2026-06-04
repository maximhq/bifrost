import { createFileRoute } from "@tanstack/react-router";
import ScopedPricingOverridesPage from "./page";

export const Route = createFileRoute("/workspace/custom-pricing/overrides")({
	component: ScopedPricingOverridesPage,
});